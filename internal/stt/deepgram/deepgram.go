package deepgram

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"time"

	"github.com/deepgram/deepgram-go-sdk/pkg/client/prerecorded"
	"github.com/google/uuid"
	"github.com/user/discord-notetaker/internal/audio"
	"github.com/rs/zerolog/log"
)

type DeepgramTranscriber struct {
	client       *prerecorded.Client
	model        string
	diarize      bool
	punctuate    bool
	utterances   bool
}

func NewDeepgramTranscriber(apiKey, model string, diarize, punctuate, utterances bool) *DeepgramTranscriber {
	client := prerecorded.NewWithDefaults()
	client.ApiKey = apiKey

	return &DeepgramTranscriber{
		client:     client,
		model:      model,
		diarize:    diarize,
		punctuate:  punctuate,
		utterances: utterances,
	}
}

func (d *DeepgramTranscriber) Transcribe(ctx context.Context, chunk *audio.Chunk) ([]audio.Utterance, error) {
	if len(chunk.PCM) == 0 {
		return nil, nil
	}

	// Convert PCM to WAV format for Deepgram
	wavData, err := d.pcmToWAV(chunk.PCM, 48000)
	if err != nil {
		return nil, fmt.Errorf("failed to convert PCM to WAV: %w", err)
	}

	// Prepare transcription options
	options := prerecorded.TranscriptionOptions{
		Model:          d.model,
		Punctuate:     d.punctuate,
		Diarize:       d.diarize,
		Utterances:    d.utterances,
		SmartFormat:   true,
		Language:      "en",
		Encoding:      "wav",
		SampleRate:    48000,
		Channels:      1,
	}

	// Call Deepgram API
	result, err := d.client.TranscribeFile(ctx, wavData, &options)
	if err != nil {
		return nil, fmt.Errorf("Deepgram transcription failed: %w", err)
	}

	if result == nil || len(result.Results.Channels) == 0 {
		return nil, nil
	}

	var utterances []audio.Utterance
	channel := result.Results.Channels[0]

	// Process alternatives (usually just one)
	for _, alternative := range channel.Alternatives {
		if alternative.Transcript == "" {
			continue
		}

		// Create single utterance for whole chunk
		utt := audio.Utterance{
			ID:         uuid.New(),
			TSStart:    chunk.Start,
			TSEnd:      chunk.End,
			Text:       alternative.Transcript,
			Source:     "deepgram",
			Confidence: alternative.Confidence,
		}

		if len(chunk.Speakers) > 0 {
			utt.UserID = chunk.Speakers[0]
			utt.UserTag = chunk.Speakers[0]
		}

		utterances = append(utterances, utt)
	}

	log.Debug().
		Str("chunk_id", chunk.ID.String()).
		Int("utterances", len(utterances)).
		Msg("Deepgram transcription completed")

	return utterances, nil
}

func (d *DeepgramTranscriber) pcmToWAV(pcm []int16, sampleRate int) ([]byte, error) {
	buf := new(bytes.Buffer)

	// WAV header
	// ChunkID
	buf.WriteString("RIFF")
	
	// ChunkSize (will be updated later)
	chunkSizePos := buf.Len()
	binary.Write(buf, binary.LittleEndian, uint32(0))
	
	// Format
	buf.WriteString("WAVE")
	
	// Subchunk1ID
	buf.WriteString("fmt ")
	
	// Subchunk1Size
	binary.Write(buf, binary.LittleEndian, uint32(16))
	
	// AudioFormat (PCM = 1)
	binary.Write(buf, binary.LittleEndian, uint16(1))
	
	// NumChannels
	binary.Write(buf, binary.LittleEndian, uint16(1))
	
	// SampleRate
	binary.Write(buf, binary.LittleEndian, uint32(sampleRate))
	
	// ByteRate
	byteRate := uint32(sampleRate * 1 * 2) // sampleRate * channels * bytesPerSample
	binary.Write(buf, binary.LittleEndian, byteRate)
	
	// BlockAlign
	binary.Write(buf, binary.LittleEndian, uint16(2))
	
	// BitsPerSample
	binary.Write(buf, binary.LittleEndian, uint16(16))
	
	// Subchunk2ID
	buf.WriteString("data")
	
	// Subchunk2Size
	dataSize := uint32(len(pcm) * 2)
	binary.Write(buf, binary.LittleEndian, dataSize)
	
	// Write PCM data
	for _, sample := range pcm {
		binary.Write(buf, binary.LittleEndian, sample)
	}

	// Update ChunkSize
	wavData := buf.Bytes()
	chunkSize := uint32(len(wavData) - 8)
	binary.LittleEndian.PutUint32(wavData[chunkSizePos:chunkSizePos+4], chunkSize)

	return wavData, nil
}

func (d *DeepgramTranscriber) Close() error {
	// Deepgram client doesn't require explicit cleanup
	return nil
}