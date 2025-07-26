package bot

import (
	"fmt"
	"os"
	"strings"
	"sync"

	"github.com/bwmarrin/discordgo"
	"github.com/user/discord-notetaker/internal/audio"
	"github.com/user/discord-notetaker/internal/config"
	"github.com/user/discord-notetaker/internal/stt"
	"github.com/user/discord-notetaker/internal/stt/deepgram"
	"github.com/user/discord-notetaker/internal/stt/vosk"
	"github.com/user/discord-notetaker/internal/store"
	"github.com/user/discord-notetaker/internal/summariser/gemini"
	"github.com/rs/zerolog/log"
)

type Bot struct {
	config      *config.Config
	session     *discordgo.Session
	store       *store.FileStore
	summariser  *gemini.GeminiSummariser
	transcriber stt.Transcriber

	// Active sessions
	sessions map[string]*VoiceSession
	mutex    sync.RWMutex
}

func NewBot(cfg *config.Config) (*Bot, error) {
	// Create Discord session
	session, err := discordgo.New("Bot " + cfg.DiscordToken)
	if err != nil {
		return nil, fmt.Errorf("failed to create Discord session: %w", err)
	}

	// Set intents
	session.Identify.Intents = discordgo.IntentsGuilds |
		discordgo.IntentsGuildMessages |
		discordgo.IntentsGuildVoiceStates |
		discordgo.IntentsMessageContent

	// Create store
	store, err := store.NewFileStore("./data")
	if err != nil {
		return nil, fmt.Errorf("failed to create store: %w", err)
	}

	// Create summariser
	summariser, err := gemini.NewGeminiSummariser(cfg.GenAIAPIKey, cfg.GenAIModel)
	if err != nil {
		return nil, fmt.Errorf("failed to create summariser: %w", err)
	}

	// Create transcriber based on config
	var transcriber stt.Transcriber
	switch cfg.STTBackend {
	case "vosk":
		transcriber, err = vosk.NewVoskTranscriber(cfg.VoskModelPath, audio.SampleRate)
		if err != nil {
			return nil, fmt.Errorf("failed to create Vosk transcriber: %w", err)
		}
	case "deepgram":
		transcriber = deepgram.NewDeepgramTranscriber(
			cfg.DeepgramAPIKey,
			cfg.DeepgramTier,
			cfg.DeepgramDiarize,
			cfg.DeepgramPunctuate,
			cfg.DeepgramUtterances,
		)
	default:
		return nil, fmt.Errorf("unsupported STT backend: %s", cfg.STTBackend)
	}

	bot := &Bot{
		config:      cfg,
		session:     session,
		store:       store,
		summariser:  summariser,
		transcriber: transcriber,
		sessions:    make(map[string]*VoiceSession),
	}

	// Register handlers
	session.AddHandler(bot.onReady)
	session.AddHandler(bot.onMessageCreate)

	return bot, nil
}

func (b *Bot) Start() error {
	// Open connection
	if err := b.session.Open(); err != nil {
		return fmt.Errorf("failed to open Discord session: %w", err)
	}

	log.Info().Msg("Discord bot started")
	return nil
}

func (b *Bot) Stop() error {
	// Stop all active sessions
	b.mutex.Lock()
	for _, session := range b.sessions {
		session.Stop()
	}
	b.sessions = make(map[string]*VoiceSession)
	b.mutex.Unlock()

	// Close Discord session
	if err := b.session.Close(); err != nil {
		return fmt.Errorf("failed to close Discord session: %w", err)
	}

	// Close transcriber
	if b.transcriber != nil {
		b.transcriber.Close()
	}

	// Close summariser
	if b.summariser != nil {
		b.summariser.Close()
	}

	log.Info().Msg("Discord bot stopped")
	return nil
}

func (b *Bot) onReady(s *discordgo.Session, event *discordgo.Ready) {
	log.Info().
		Str("username", event.User.Username).
		Int("guilds", len(event.Guilds)).
		Msg("Bot is ready")
}

func (b *Bot) onMessageCreate(s *discordgo.Session, m *discordgo.MessageCreate) {
	// Ignore bot messages
	if m.Author.Bot {
		return
	}

	// Check for commands
	content := strings.TrimSpace(m.Content)
	
	switch {
	case strings.HasPrefix(content, "!join"):
		b.handleJoin(s, m)
	case strings.HasPrefix(content, "!leave"):
		b.handleLeave(s, m)
	}
}

func (b *Bot) handleJoin(s *discordgo.Session, m *discordgo.MessageCreate) {
	// Find user's voice channel
	guild, err := s.State.Guild(m.GuildID)
	if err != nil {
		b.sendError(s, m.ChannelID, "Failed to get guild information")
		return
	}

	var voiceChannelID string
	for _, voiceState := range guild.VoiceStates {
		if voiceState.UserID == m.Author.ID {
			voiceChannelID = voiceState.ChannelID
			break
		}
	}

	if voiceChannelID == "" {
		b.sendError(s, m.ChannelID, "You need to be in a voice channel to use this command")
		return
	}

	// Check if already recording in this guild
	b.mutex.RLock()
	for _, session := range b.sessions {
		if session.GuildID == m.GuildID {
			b.mutex.RUnlock()
			b.sendError(s, m.ChannelID, "Already recording in this server")
			return
		}
	}
	b.mutex.RUnlock()

	// Create new session
	sessionID := store.GenerateSessionID()
	
	// Create audio components
	decoder, err := audio.NewOpusDecoder()
	if err != nil {
		b.sendError(s, m.ChannelID, "Failed to create audio decoder")
		return
	}

	vad, err := audio.NewWebRTCVAD()
	if err != nil {
		b.sendError(s, m.ChannelID, "Failed to create voice activity detector")
		return
	}

	chunker := audio.NewRingChunker(
		b.config.ChunkSeconds,
		b.config.ChunkOverlapMS,
		audio.SampleRate,
	)

	transcriberPool := stt.NewTranscriberPool(b.transcriber, b.config.MaxParallelSTT)

	session := NewVoiceSession(
		sessionID,
		m.GuildID,
		voiceChannelID,
		m.ChannelID,
		m.Author.ID,
		s,
		decoder,
		vad,
		chunker,
		transcriberPool,
		b.summariser,
		b.store,
	)

	// Start session
	if err := session.Start(); err != nil {
		b.sendError(s, m.ChannelID, fmt.Sprintf("Failed to start recording: %v", err))
		return
	}

	// Store session
	b.mutex.Lock()
	b.sessions[sessionID] = session
	b.mutex.Unlock()

	// Send confirmation
	s.ChannelMessageSend(m.ChannelID, fmt.Sprintf("🎙️ Started recording in <#%s>. Use `!leave` to stop.", voiceChannelID))

	log.Info().
		Str("session_id", sessionID).
		Str("guild_id", m.GuildID).
		Str("channel_id", voiceChannelID).
		Str("user_id", m.Author.ID).
		Msg("Started voice recording session")
}

func (b *Bot) handleLeave(s *discordgo.Session, m *discordgo.MessageCreate) {
	// Find active session in this guild
	b.mutex.RLock()
	var session *VoiceSession
	for _, sess := range b.sessions {
		if sess.GuildID == m.GuildID {
			session = sess
			break
		}
	}
	b.mutex.RUnlock()

	if session == nil {
		b.sendError(s, m.ChannelID, "No active recording session in this server")
		return
	}

	// Stop session
	if err := session.Stop(); err != nil {
		b.sendError(s, m.ChannelID, fmt.Sprintf("Failed to stop recording: %v", err))
		return
	}

	// Remove from active sessions
	b.mutex.Lock()
	delete(b.sessions, session.ID)
	b.mutex.Unlock()

	// Send processing message
	processingMsg, _ := s.ChannelMessageSend(m.ChannelID, "⏳ Processing recording and generating notes...")

	// Finalize and save
	transcriptPath, notesPath, err := session.Finalize()
	if err != nil {
		b.sendError(s, m.ChannelID, fmt.Sprintf("Failed to process recording: %v", err))
		return
	}

	// Update message
	s.ChannelMessageEdit(m.ChannelID, processingMsg.ID, "✅ Recording processed!")

	// Send files
	b.sendFiles(s, m.ChannelID, transcriptPath, notesPath)

	log.Info().
		Str("session_id", session.ID).
		Str("transcript_path", transcriptPath).
		Str("notes_path", notesPath).
		Msg("Completed voice recording session")
}

func (b *Bot) sendError(s *discordgo.Session, channelID, message string) {
	s.ChannelMessageSend(channelID, "❌ "+message)
	log.Warn().Str("channel_id", channelID).Str("error", message).Msg("Sent error message")
}

func (b *Bot) sendFiles(s *discordgo.Session, channelID, transcriptPath, notesPath string) {
	// Read files
	transcriptData, err := os.ReadFile(transcriptPath)
	if err != nil {
		b.sendError(s, channelID, "Failed to read transcript file")
		return
	}

	notesData, err := os.ReadFile(notesPath)
	if err != nil {
		b.sendError(s, channelID, "Failed to read notes file")
		return
	}

	// Send as message with files
	_, err = s.ChannelMessageSendComplex(channelID, &discordgo.MessageSend{
		Content: "📝 Here are your meeting notes and transcript:",
		Files: []*discordgo.File{
			{
				Name:        "transcript.jsonl",
				ContentType: "application/jsonl",
				Reader:      strings.NewReader(string(transcriptData)),
			},
			{
				Name:        "notes.md",
				ContentType: "text/markdown",
				Reader:      strings.NewReader(string(notesData)),
			},
		},
	})

	if err != nil {
		b.sendError(s, channelID, "Failed to send files")
	}
}