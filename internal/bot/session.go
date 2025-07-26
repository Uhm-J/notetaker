package bot

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/bwmarrin/discordgo"
	"github.com/rs/zerolog/log"
	"github.com/user/discord-notetaker/internal/audio"
	"github.com/user/discord-notetaker/internal/store"
	"github.com/user/discord-notetaker/internal/stt"
	"github.com/user/discord-notetaker/internal/summariser/gemini"
	"golang.org/x/sync/errgroup"
)

type VoiceSession struct {
	ID            string
	GuildID       string
	ChannelID     string
	TextChannelID string
	UserID        string // User who initiated the session

	// Audio pipeline components (used as templates)
	decoder     audio.AudioDecoder
	vad         audio.VAD
	transcriber *stt.TranscriberPool
	summariser  *gemini.GeminiSummariser

	// Per-speaker audio processing
	speakerChunkers map[uint32]audio.Chunker // SSRC -> individual chunker
	speakerMap      map[uint32]string        // SSRC -> UserID mapping
	speakerMux      sync.RWMutex             // Protects speakerChunkers and speakerMap

	// Chunker template for creating new per-speaker chunkers
	chunkerTemplate audio.Chunker

	// Discord
	session   *discordgo.Session
	voiceConn *discordgo.VoiceConnection

	// Storage
	store      *store.FileStore
	utterances []audio.Utterance

	// Control
	ctx     context.Context
	cancel  context.CancelFunc
	stopped bool
	mutex   sync.RWMutex

	// STT Worker Pool
	sttWorkerPool *errgroup.Group
	sttCtx        context.Context
	sttCancel     context.CancelFunc
}

func NewVoiceSession(
	id, guildID, channelID, textChannelID, userID string,
	session *discordgo.Session,
	decoder audio.AudioDecoder,
	vad audio.VAD,
	chunker audio.Chunker, // This will be used as a template for per-speaker chunkers
	transcriber *stt.TranscriberPool,
	summariser *gemini.GeminiSummariser,
	store *store.FileStore,
) *VoiceSession {
	ctx, cancel := context.WithCancel(context.Background())

	return &VoiceSession{
		ID:              id,
		GuildID:         guildID,
		ChannelID:       channelID,
		TextChannelID:   textChannelID,
		UserID:          userID,
		decoder:         decoder,
		vad:             vad,
		chunkerTemplate: chunker,
		transcriber:     transcriber,
		summariser:      summariser,
		session:         session,
		store:           store,
		ctx:             ctx,
		cancel:          cancel,
		utterances:      make([]audio.Utterance, 0),
		speakerChunkers: make(map[uint32]audio.Chunker),
		speakerMap:      make(map[uint32]string),
		speakerMux:      sync.RWMutex{},
	}
}

func (vs *VoiceSession) Start() error {
	vs.mutex.Lock()
	defer vs.mutex.Unlock()

	if vs.stopped {
		return fmt.Errorf("session already stopped")
	}

	// Connect to voice channel
	// Parameters: guildID, channelID, mute, deaf
	// mute: false (bot can send audio if needed)
	// deaf: false (bot MUST be able to receive audio to transcribe it)
	voiceConn, err := vs.session.ChannelVoiceJoin(vs.GuildID, vs.ChannelID, false, false)
	if err != nil {
		return fmt.Errorf("failed to join voice channel: %w", err)
	}
	vs.voiceConn = voiceConn

	// CRITICAL: Add speaking update handler BEFORE setting up audio reception
	// This captures SSRC-to-UserID mappings as per Discord voice documentation
	vs.voiceConn.AddHandler(vs.handleSpeakingUpdate)

	// Wait for voice connection to be ready
	for !vs.voiceConn.Ready {
		time.Sleep(10 * time.Millisecond)
	}

	// CRITICAL: Send initial speaking state to Discord to establish SSRC mapping
	// This is required according to Discord documentation before receiving audio
	err = vs.voiceConn.Speaking(false)
	if err != nil {
		log.Warn().
			Str("session_id", vs.ID).
			Err(err).
			Msg("Failed to send initial speaking state")
	} else {
		log.Info().
			Str("session_id", vs.ID).
			Msg("Sent initial speaking state to Discord")
	}

	log.Info().
		Str("session_id", vs.ID).
		Str("guild_id", vs.GuildID).
		Str("channel_id", vs.ChannelID).
		Msg("Voice connection established and speaking handler registered")

	// Start transcriber pool
	if err := vs.transcriber.Start(vs.ctx); err != nil {
		return fmt.Errorf("failed to start transcriber: %w", err)
	}

	// Start processing goroutines
	go vs.processAudioLoop()
	go vs.processUtterances()

	log.Info().
		Str("session_id", vs.ID).
		Str("channel_id", vs.ChannelID).
		Msg("Voice session started")

	return nil
}

func (vs *VoiceSession) processAudioLoop() {
	defer log.Debug().Str("session_id", vs.ID).Msg("Audio processing stopped")

	for {
		select {
		case packet, ok := <-vs.voiceConn.OpusRecv:
			if !ok {
				log.Info().Str("session_id", vs.ID).Msg("Voice receive channel closed")
				return
			}
			vs.processAudioPacket(packet)
		case <-vs.ctx.Done():
			log.Info().Str("session_id", vs.ID).Msg("Audio processing context cancelled")
			return
		}
	}
}

func (vs *VoiceSession) processAudioPacket(packet *discordgo.Packet) {
	if vs.stopped {
		return
	}

	log.Debug().
		Str("session_id", vs.ID).
		Uint32("ssrc", packet.SSRC).
		Int("opus_size", len(packet.Opus)).
		Msg("Processing audio packet")

	// Decode Opus to PCM
	pcm, err := vs.decoder.Decode(packet.Opus)
	if err != nil {
		log.Warn().
			Str("session_id", vs.ID).
			Uint32("ssrc", packet.SSRC).
			Err(err).
			Msg("Failed to decode opus packet")
		return
	}

	// Apply VAD
	if !vs.vad.IsSpeech(pcm, 48000) {
		log.Debug().
			Str("session_id", vs.ID).
			Uint32("ssrc", packet.SSRC).
			Msg("VAD detected silence - skipping packet")
		return
	}

	log.Debug().
		Str("session_id", vs.ID).
		Uint32("ssrc", packet.SSRC).
		Int("pcm_samples", len(pcm)).
		Msg("VAD detected speech - processing packet")

		// Get current speakers for this SSRC
	speakers := vs.getCurrentSpeakers(packet.SSRC)

	log.Info().
		Str("session_id", vs.ID).
		Uint32("ssrc", packet.SSRC).
		Strs("speakers", speakers).
		Msg("SSRC mapped to speakers")

	// Get or create chunker for this SSRC and add samples
	chunker := vs.getOrCreateChunkerForSSRC(packet.SSRC)
	timestamp := time.Now()
	chunker.AddSamples(pcm, timestamp, speakers)

}

// processChunks is now handled per-speaker in processChunksForSpeaker

func (vs *VoiceSession) processUtterances() {
	defer log.Debug().Str("session_id", vs.ID).Msg("Utterance processing stopped")

	for {
		select {
		case utterances, ok := <-vs.transcriber.GetUtterances():
			if !ok {
				return
			}

			vs.mutex.Lock()
			// Resolve user tags
			for i := range utterances {
				if utterances[i].UserID != "" {
					if user, err := vs.session.User(utterances[i].UserID); err == nil {
						utterances[i].UserTag = user.Username
					} else {
						utterances[i].UserTag = utterances[i].UserID
					}
				}
			}

			vs.utterances = append(vs.utterances, utterances...)
			vs.mutex.Unlock()

			log.Debug().
				Int("new_utterances", len(utterances)).
				Int("total_utterances", len(vs.utterances)).
				Str("session_id", vs.ID).
				Msg("Added utterances")

		case <-vs.ctx.Done():
			return
		}
	}
}

func (vs *VoiceSession) handleSpeakingUpdate(vc *discordgo.VoiceConnection, vs2 *discordgo.VoiceSpeakingUpdate) {
	if vs2 == nil {
		return
	}

	vs.speakerMux.Lock()
	defer vs.speakerMux.Unlock()

	if vs2.Speaking {
		// User started speaking - map their SSRC to their UserID
		vs.speakerMap[uint32(vs2.SSRC)] = vs2.UserID
		log.Info().
			Str("session_id", vs.ID).
			Uint32("ssrc", uint32(vs2.SSRC)).
			Str("user_id", vs2.UserID).
			Int("total_mappings", len(vs.speakerMap)).
			Msg("User started speaking - added SSRC mapping")
	} else {
		// User stopped speaking - we can keep the mapping for future use
		log.Info().
			Str("session_id", vs.ID).
			Uint32("ssrc", uint32(vs2.SSRC)).
			Str("user_id", vs2.UserID).
			Msg("User stopped speaking - keeping SSRC mapping")
	}

	// Log current speaker mappings
	log.Debug().
		Str("session_id", vs.ID).
		Interface("speaker_map", vs.speakerMap).
		Msg("Current speaker mappings")
}

func (vs *VoiceSession) getCurrentSpeakers(ssrc uint32) []string {
	log.Debug().
		Str("session_id", vs.ID).
		Uint32("ssrc", ssrc).
		Msg("Getting speakers for SSRC")

	vs.speakerMux.RLock()
	speaker, ok := vs.speakerMap[ssrc]
	vs.speakerMux.RUnlock()

	if ok {
		log.Debug().
			Str("session_id", vs.ID).
			Uint32("ssrc", ssrc).
			Str("user_id", speaker).
			Msg("Found direct SSRC mapping")
		return []string{speaker}
	}

	// Try to auto-map this SSRC to an unmapped user
	if mappedUser := vs.autoMapSSRCToUser(ssrc); mappedUser != "" {
		log.Debug().
			Str("session_id", vs.ID).
			Uint32("ssrc", ssrc).
			Str("auto_mapped_user_id", mappedUser).
			Msg("Auto-mapped SSRC to user")
		return []string{mappedUser}
	}

	// Fallback: return first available user in voice channel
	if vs.voiceConn == nil {
		return []string{}
	}

	guild, err := vs.session.State.Guild(vs.GuildID)
	if err != nil {
		return []string{}
	}

	for _, voiceState := range guild.VoiceStates {
		if voiceState.ChannelID == vs.ChannelID {
			log.Warn().
				Str("session_id", vs.ID).
				Uint32("ssrc", ssrc).
				Str("fallback_user_id", voiceState.UserID).
				Msg("Using fallback speaker detection")
			return []string{voiceState.UserID}
		}
	}

	return []string{}
}

func (vs *VoiceSession) autoMapSSRCToUser(ssrc uint32) string {
	log.Debug().
		Str("session_id", vs.ID).
		Uint32("ssrc", ssrc).
		Msg("Attempting auto-mapping based on speaking order")

	vs.speakerMux.Lock()
	defer vs.speakerMux.Unlock()

	// Get all users in the voice channel
	guild, err := vs.session.State.Guild(vs.GuildID)
	if err != nil {
		return ""
	}

	var usersInChannel []string
	for _, voiceState := range guild.VoiceStates {
		if voiceState.ChannelID == vs.ChannelID {
			usersInChannel = append(usersInChannel, voiceState.UserID)
		}
	}

	// Find users that aren't mapped to any SSRC yet
	mappedUsers := make(map[string]bool)
	for _, userID := range vs.speakerMap {
		mappedUsers[userID] = true
	}

	// Strategy: Instead of using guild order, try to be smarter about mapping
	// If we only have 2 users and this is the second SSRC, map to the unmapped user
	unmappedUsers := []string{}
	for _, userID := range usersInChannel {
		if !mappedUsers[userID] {
			unmappedUsers = append(unmappedUsers, userID)
		}
	}

	if len(unmappedUsers) > 0 {
		// Take the first unmapped user
		userID := unmappedUsers[0]
		vs.speakerMap[ssrc] = userID
		log.Info().
			Str("session_id", vs.ID).
			Uint32("ssrc", ssrc).
			Str("user_id", userID).
			Int("unmapped_users_remaining", len(unmappedUsers)-1).
			Msg("Auto-mapped SSRC to next unmapped user")
		return userID
	}

	log.Debug().
		Str("session_id", vs.ID).
		Uint32("ssrc", ssrc).
		Int("users_in_channel", len(usersInChannel)).
		Int("mapped_ssrcs", len(vs.speakerMap)).
		Msg("Could not auto-map SSRC - all users already mapped")
	return ""
}

func (vs *VoiceSession) Stop() error {
	vs.mutex.Lock()
	defer vs.mutex.Unlock()

	if vs.stopped {
		return nil
	}

	vs.stopped = true
	vs.cancel()

	// Stop all speaker chunkers
	vs.speakerMux.RLock()
	for ssrc, chunker := range vs.speakerChunkers {
		if chunker != nil {
			chunker.Stop()
			log.Debug().
				Str("session_id", vs.ID).
				Uint32("ssrc", ssrc).
				Msg("Stopped speaker chunker")
		}
	}
	vs.speakerMux.RUnlock()

	if vs.transcriber != nil {
		vs.transcriber.Stop()
	}

	// Disconnect from voice
	if vs.voiceConn != nil {
		vs.voiceConn.Disconnect()
	}

	log.Info().
		Str("session_id", vs.ID).
		Int("total_utterances", len(vs.utterances)).
		Msg("Voice session stopped")

	return nil
}

func (vs *VoiceSession) Finalize() (string, string, error) {
	vs.mutex.RLock()
	utterances := make([]audio.Utterance, len(vs.utterances))
	copy(utterances, vs.utterances)
	vs.mutex.RUnlock()

	// Map User IDs to usernames using Discord Guild Members API
	log.Info().
		Str("session_id", vs.ID).
		Msg("Resolving User IDs to usernames using Guild Members API")

	for i := range utterances {
		if utterances[i].UserID != "" {
			// Get user info from Discord API
			member, err := vs.session.GuildMember(vs.GuildID, utterances[i].UserID)
			if err != nil {
				// Fallback to User API if Guild Member fails
				user, userErr := vs.session.User(utterances[i].UserID)
				if userErr == nil {
					utterances[i].UserTag = user.Username
					log.Debug().
						Str("session_id", vs.ID).
						Str("user_id", utterances[i].UserID).
						Str("username", user.Username).
						Msg("Resolved User ID to username via User API")
				} else {
					log.Warn().
						Str("session_id", vs.ID).
						Str("user_id", utterances[i].UserID).
						Err(err).
						Err(userErr).
						Msg("Failed to resolve User ID to username")
					utterances[i].UserTag = "Unknown User"
				}
			} else {
				// Use nickname if available, otherwise username
				displayName := member.User.Username
				if member.Nick != "" {
					displayName = member.Nick
				}
				utterances[i].UserTag = displayName
				log.Debug().
					Str("session_id", vs.ID).
					Str("user_id", utterances[i].UserID).
					Str("username", member.User.Username).
					Str("nickname", member.Nick).
					Str("display_name", displayName).
					Msg("Resolved User ID to username via Guild Member API")
			}
		}
	}

	// Save transcript
	transcriptPath, err := vs.store.SaveTranscript(vs.ID, utterances)
	if err != nil {
		return "", "", fmt.Errorf("failed to save transcript: %w", err)
	}

	// Generate and save notes
	// Use a fresh context for summarization since the session context may be cancelled
	summaryCtx := context.Background()
	notes, err := vs.summariser.Summarise(summaryCtx, utterances)
	if err != nil {
		return "", "", fmt.Errorf("failed to generate notes: %w", err)
	}

	notesPath, err := vs.store.SaveNotes(vs.ID, notes)
	if err != nil {
		return "", "", fmt.Errorf("failed to save notes: %w", err)
	}

	return transcriptPath, notesPath, nil
}

func (vs *VoiceSession) getOrCreateChunkerForSSRC(ssrc uint32) audio.Chunker {
	vs.speakerMux.Lock()
	defer vs.speakerMux.Unlock()

	// Check if we already have a chunker for this speaker
	if chunker, exists := vs.speakerChunkers[ssrc]; exists {
		return chunker
	}

	// Create a new chunker for this speaker based on the template
	// We need to create a new instance, not reuse the template
	newChunker := audio.NewRingChunker(10, 500, 48000) // 10s chunks, 500ms overlap, 48kHz
	vs.speakerChunkers[ssrc] = newChunker

	// Start processing chunks from this speaker
	go vs.processChunksForSpeaker(ssrc, newChunker)

	log.Debug().
		Str("session_id", vs.ID).
		Uint32("ssrc", ssrc).
		Msg("Created new chunker for speaker")

	return newChunker
}

func (vs *VoiceSession) processChunksForSpeaker(ssrc uint32, chunker audio.Chunker) {
	defer log.Debug().
		Str("session_id", vs.ID).
		Uint32("ssrc", ssrc).
		Msg("Speaker chunk processing stopped")

	chunkCount := 0
	for {
		select {
		case chunk, ok := <-chunker.GetChunk():
			if !ok {
				return
			}

			chunkCount++
			log.Debug().
				Str("session_id", vs.ID).
				Uint32("ssrc", ssrc).
				Str("chunk_id", chunk.ID.String()).
				Int("chunk_count", chunkCount).
				Int("pcm_samples", len(chunk.PCM)).
				Strs("speakers", chunk.Speakers).
				Msg("Received audio chunk from speaker")

			// Send chunk to transcriber pool
			if err := vs.transcriber.ProcessChunk(chunk); err != nil {
				log.Warn().
					Err(err).
					Str("chunk_id", chunk.ID.String()).
					Str("session_id", vs.ID).
					Uint32("ssrc", ssrc).
					Msg("Failed to process speaker chunk")
			} else {
				log.Debug().
					Str("session_id", vs.ID).
					Uint32("ssrc", ssrc).
					Str("chunk_id", chunk.ID.String()).
					Msg("Sent speaker chunk to transcriber pool")
			}

		case <-vs.ctx.Done():
			return
		}
	}
}

func (vs *VoiceSession) refreshSpeakerMappings() {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			vs.updateSpeakerMappingsFromVoiceStates()
		case <-vs.ctx.Done():
			return
		}
	}
}

func (vs *VoiceSession) updateSpeakerMappingsFromVoiceStates() {
	guild, err := vs.session.State.Guild(vs.GuildID)
	if err != nil {
		log.Warn().
			Str("session_id", vs.ID).
			Err(err).
			Msg("Failed to get guild for speaker mapping refresh")
		return
	}

	// Get all users currently in our voice channel
	var usersInChannel []string
	for _, voiceState := range guild.VoiceStates {
		if voiceState.ChannelID == vs.ChannelID {
			usersInChannel = append(usersInChannel, voiceState.UserID)
		}
	}

	vs.speakerMux.Lock()
	currentMappings := len(vs.speakerMap)
	vs.speakerMux.Unlock()

	log.Debug().
		Str("session_id", vs.ID).
		Strs("users_in_channel", usersInChannel).
		Int("current_mappings", currentMappings).
		Msg("Refreshing speaker mappings")

	// If we have fewer mappings than users, and we've seen audio packets,
	// we might be missing some speaker mappings, but we take into account the bot itself
	if len(usersInChannel)-1 > currentMappings {
		log.Warn().
			Str("session_id", vs.ID).
			Int("users_in_channel", len(usersInChannel)).
			Int("mapped_speakers", currentMappings).
			Msg("Possible missing speaker mappings detected")
	}
}
