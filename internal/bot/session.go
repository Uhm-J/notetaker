package bot

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/bwmarrin/discordgo"
	"github.com/user/discord-notetaker/internal/audio"
	"github.com/user/discord-notetaker/internal/stt"
	"github.com/user/discord-notetaker/internal/store"
	"github.com/user/discord-notetaker/internal/summariser/gemini"
	"github.com/rs/zerolog/log"
)

type VoiceSession struct {
	ID            string
	GuildID       string
	ChannelID     string
	TextChannelID string
	UserID        string // User who initiated the session

	// Audio pipeline
	decoder      audio.AudioDecoder
	vad          audio.VAD
	chunker      audio.Chunker
	transcriber  *stt.TranscriberPool
	summariser   *gemini.GeminiSummariser

	// Discord
	session    *discordgo.Session
	voiceConn  *discordgo.VoiceConnection

	// Storage
	store      *store.FileStore
	utterances []audio.Utterance

	// Control
	ctx        context.Context
	cancel     context.CancelFunc
	stopped    bool
	mutex      sync.RWMutex
}

func NewVoiceSession(
	id, guildID, channelID, textChannelID, userID string,
	session *discordgo.Session,
	decoder audio.AudioDecoder,
	vad audio.VAD,
	chunker audio.Chunker,
	transcriber *stt.TranscriberPool,
	summariser *gemini.GeminiSummariser,
	store *store.FileStore,
) *VoiceSession {
	ctx, cancel := context.WithCancel(context.Background())

	return &VoiceSession{
		ID:            id,
		GuildID:       guildID,
		ChannelID:     channelID,
		TextChannelID: textChannelID,
		UserID:        userID,
		decoder:       decoder,
		vad:           vad,
		chunker:       chunker,
		transcriber:   transcriber,
		summariser:    summariser,
		session:       session,
		store:         store,
		ctx:           ctx,
		cancel:        cancel,
		utterances:    make([]audio.Utterance, 0),
	}
}

func (vs *VoiceSession) Start() error {
	vs.mutex.Lock()
	defer vs.mutex.Unlock()

	if vs.stopped {
		return fmt.Errorf("session already stopped")
	}

	// Connect to voice channel
	voiceConn, err := vs.session.ChannelVoiceJoin(vs.GuildID, vs.ChannelID, false, true)
	if err != nil {
		return fmt.Errorf("failed to join voice channel: %w", err)
	}
	vs.voiceConn = voiceConn

	// Start transcriber pool
	if err := vs.transcriber.Start(vs.ctx); err != nil {
		return fmt.Errorf("failed to start transcriber: %w", err)
	}

	// Start processing goroutines
	go vs.processAudio()
	go vs.processChunks()
	go vs.processUtterances()

	log.Info().
		Str("session_id", vs.ID).
		Str("channel_id", vs.ChannelID).
		Msg("Voice session started")

	return nil
}

func (vs *VoiceSession) processAudio() {
	defer log.Debug().Str("session_id", vs.ID).Msg("Audio processing stopped")

	for {
		select {
		case packet, ok := <-vs.voiceConn.OpusRecv:
			if !ok {
				return
			}

			// Decode Opus to PCM
			pcm, err := vs.decoder.Decode(packet.Opus)
			if err != nil {
				log.Warn().Err(err).Msg("Failed to decode opus packet")
				continue
			}

			// Get current speakers from voice states
			speakers := vs.getCurrentSpeakers(packet.SSRC)

			// Add to chunker
			vs.chunker.AddSamples(pcm, time.Now(), speakers)

		case <-vs.ctx.Done():
			return
		}
	}
}

func (vs *VoiceSession) processChunks() {
	defer log.Debug().Str("session_id", vs.ID).Msg("Chunk processing stopped")

	for {
		select {
		case chunk, ok := <-vs.chunker.GetChunk():
			if !ok {
				return
			}

			// Send chunk to transcriber pool
			if err := vs.transcriber.ProcessChunk(chunk); err != nil {
				log.Warn().
					Err(err).
					Str("chunk_id", chunk.ID.String()).
					Msg("Failed to process chunk")
			}

		case <-vs.ctx.Done():
			return
		}
	}
}

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

func (vs *VoiceSession) getCurrentSpeakers(ssrc uint32) []string {
	// Find user associated with this SSRC
	if vs.voiceConn == nil {
		return []string{}
	}

	// Look through voice states to find speaker
	guild, err := vs.session.State.Guild(vs.GuildID)
	if err != nil {
		return []string{}
	}

	for _, voiceState := range guild.VoiceStates {
		if voiceState.ChannelID == vs.ChannelID {
			// This is a simplified approach - in reality you'd need to map SSRC to user
			// Discord doesn't provide direct SSRC-to-user mapping
			return []string{voiceState.UserID}
		}
	}

	return []string{}
}

func (vs *VoiceSession) Stop() error {
	vs.mutex.Lock()
	defer vs.mutex.Unlock()

	if vs.stopped {
		return nil
	}

	vs.stopped = true
	vs.cancel()

	// Stop audio processing
	if vs.chunker != nil {
		vs.chunker.Stop()
	}

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

	// Save transcript
	transcriptPath, err := vs.store.SaveTranscript(vs.ID, utterances)
	if err != nil {
		return "", "", fmt.Errorf("failed to save transcript: %w", err)
	}

	// Generate and save notes
	notes, err := vs.summariser.Summarise(vs.ctx, utterances)
	if err != nil {
		return "", "", fmt.Errorf("failed to generate notes: %w", err)
	}

	notesPath, err := vs.store.SaveNotes(vs.ID, notes)
	if err != nil {
		return "", "", fmt.Errorf("failed to save notes: %w", err)
	}

	return transcriptPath, notesPath, nil
}