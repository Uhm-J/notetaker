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
		ID:            id,
		GuildID:       guildID,
		ChannelID:     channelID,
		TextChannelID: textChannelID,
		UserID:        userID,
		decoder:       decoder,
		vad:           vad,
		chunkerTemplate: chunker,
		transcriber:   transcriber,
		summariser:    summariser,
		session:       session,
		store:         store,
		ctx:           ctx,
		cancel:        cancel,
		utterances:    make([]audio.Utterance, 0),
		speakerChunkers: make(map[uint32]audio.Chunker),
		speakerMap:    make(map[uint32]string),
		speakerMux:    sync.RWMutex{},
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

	// Start transcriber pool
	if err := vs.transcriber.Start(vs.ctx); err != nil {
		return fmt.Errorf("failed to start transcriber: %w", err)
	}

	// Set up speaking event handler for speaker tracking
	vs.voiceConn.AddHandler(vs.handleSpeakingUpdate)

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

	packetCount := 0
	log.Info().Str("session_id", vs.ID).Msg("Started audio processing - waiting for voice packets")

	for {
		select {
		case packet, ok := <-vs.voiceConn.OpusRecv:
			if !ok {
				log.Info().Str("session_id", vs.ID).Msg("Voice receive channel closed")
				return
			}

			packetCount++
			if packetCount%100 == 1 { // Log every 100th packet to avoid spam
				log.Debug().
					Str("session_id", vs.ID).
					Int("packet_count", packetCount).
					Uint32("ssrc", packet.SSRC).
					Int("opus_size", len(packet.Opus)).
					Msg("Received voice packet")
			}

			// Decode Opus to PCM
			pcm, err := vs.decoder.Decode(packet.Opus)
			if err != nil {
				log.Warn().
					Err(err).
					Str("session_id", vs.ID).
					Int("opus_size", len(packet.Opus)).
					Msg("Failed to decode opus packet")
				continue
			}

			if packetCount%100 == 1 { // Log every 100th packet
				log.Debug().
					Str("session_id", vs.ID).
					Int("pcm_size", len(pcm)).
					Msg("Decoded PCM from opus")
			}

			// Track the active speaker
			vs.speakerMux.Lock()
			vs.lastActiveSpeaker = packet.SSRC
			vs.speakerMux.Unlock()

			// Get current speakers from voice states
			speakers := vs.getCurrentSpeakers(packet.SSRC)

			// Add to chunker with speaker info
			vs.chunker.AddSamples(pcm, time.Now(), speakers)

		case <-vs.ctx.Done():
			log.Info().
				Str("session_id", vs.ID).
				Int("total_packets", packetCount).
				Msg("Audio processing context cancelled")
			return
		}
	}
}

func (vs *VoiceSession) processChunks() {
	defer log.Debug().Str("session_id", vs.ID).Msg("Chunk processing stopped")

	chunkCount := 0
	log.Info().Str("session_id", vs.ID).Msg("Started chunk processing - waiting for audio chunks")

	for {
		select {
		case chunk, ok := <-vs.chunker.GetChunk():
			if !ok {
				log.Info().Str("session_id", vs.ID).Msg("Chunk channel closed")
				return
			}

			chunkCount++
			log.Debug().
				Str("session_id", vs.ID).
				Str("chunk_id", chunk.ID.String()).
				Int("chunk_count", chunkCount).
				Int("pcm_samples", len(chunk.PCM)).
				Strs("speakers", chunk.Speakers).
				Msg("Received audio chunk for transcription")

			// Send chunk to transcriber pool
			if err := vs.transcriber.ProcessChunk(chunk); err != nil {
				log.Warn().
					Err(err).
					Str("chunk_id", chunk.ID.String()).
					Str("session_id", vs.ID).
					Msg("Failed to process chunk")
			} else {
				log.Debug().
					Str("session_id", vs.ID).
					Str("chunk_id", chunk.ID.String()).
					Msg("Sent chunk to transcriber pool")
			}

		case <-vs.ctx.Done():
			log.Info().
				Str("session_id", vs.ID).
				Int("total_chunks", chunkCount).
				Msg("Chunk processing context cancelled")
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
	vs.speakerMux.RLock()
	speaker, ok := vs.speakerMap[ssrc]
	vs.speakerMux.RUnlock()

	if ok {
		return []string{speaker}
	}

	// Fallback to voice state if SSRC-to-user mapping is not available
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

func (vs *VoiceSession) handleSpeakingUpdate(vc *discordgo.VoiceConnection, vs2 *discordgo.VoiceSpeakingUpdate) {
	if vs2 == nil {
		return
	}

	vs.speakerMux.Lock()
	defer vs.speakerMux.Unlock()

	if vs2.Speaking {
		// User started speaking - map their SSRC to their UserID
		vs.speakerMap[uint32(vs2.SSRC)] = vs2.UserID
		log.Debug().
			Str("session_id", vs.ID).
			Uint32("ssrc", uint32(vs2.SSRC)).
			Str("user_id", vs2.UserID).
			Msg("User started speaking")
	} else {
		// User stopped speaking - we can keep the mapping for future use
		log.Debug().
			Str("session_id", vs.ID).
			Uint32("ssrc", uint32(vs2.SSRC)).
			Str("user_id", vs2.UserID).
			Msg("User stopped speaking")
	}
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
