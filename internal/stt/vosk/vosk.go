package vosk

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/alphacep/vosk-api/go"
	"github.com/google/uuid"
	"github.com/user/discord-notetaker/internal/audio"
	"github.com/rs/zerolog/log"
)

type VoskTranscriber struct {
	model      *vosk.VoskModel
	recognizer *vosk.VoskRecognizer
	sampleRate int
}

type VoskResult struct {
	Text        string  `json:"text"`
	Confidence  float64 `json:"confidence"`
	Result      []VoskWord `json:"result"`
}

type VoskWord struct {
	Word  string  `json:"word"`
	Start float64 `json:"start"`
	End   float64 `json:"end"`
	Conf  float64 `json:"conf"`
}

func NewVoskTranscriber(modelPath string, sampleRate int) (*VoskTranscriber, error) {
	log.Info().Str("model_path", modelPath).Msg("Loading Vosk model")

	model, err := vosk.NewModel(modelPath)
	if err != nil {
		return nil, fmt.Errorf("failed to load Vosk model from %s: %w", modelPath, err)
	}

	recognizer, err := vosk.NewRecognizer(model, float64(sampleRate))
	if err != nil {
		model.Free()
		return nil, fmt.Errorf("failed to create Vosk recognizer: %w", err)
	}

	log.Info().Msg("Vosk model loaded successfully")

	return &VoskTranscriber{
		model:      model,
		recognizer: recognizer,
		sampleRate: sampleRate,
	}, nil
}

func (v *VoskTranscriber) Transcribe(ctx context.Context, chunk *audio.Chunk) ([]audio.Utterance, error) {
	if len(chunk.PCM) == 0 {
		return nil, nil
	}

	// Convert PCM samples to bytes
	pcmBytes := make([]byte, len(chunk.PCM)*2)
	for i, sample := range chunk.PCM {
		pcmBytes[i*2] = byte(sample)
		pcmBytes[i*2+1] = byte(sample >> 8)
	}

	// Feed audio data to recognizer
	result := v.recognizer.AcceptWaveform(pcmBytes)
	if result == -1 {
		return nil, fmt.Errorf("failed to process audio chunk")
	}

	var jsonResult string
	if result == 1 {
		// Final result available
		jsonResult = v.recognizer.Result()
	} else {
		// Partial result
		jsonResult = v.recognizer.PartialResult()
	}

	if jsonResult == "" {
		return nil, nil
	}

	var voskResult VoskResult
	if err := json.Unmarshal([]byte(jsonResult), &voskResult); err != nil {
		log.Warn().
			Err(err).
			Str("json", jsonResult).
			Msg("Failed to parse Vosk result")
		return nil, nil
	}

	if voskResult.Text == "" {
		return nil, nil
	}

	// Create utterance
	utterance := audio.Utterance{
		ID:         uuid.New(),
		TSStart:    chunk.Start,
		TSEnd:      chunk.End,
		Text:       voskResult.Text,
		Source:     "vosk",
		Confidence: voskResult.Confidence,
	}

	// If we have multiple speakers, assign to first one
	// (Vosk doesn't provide diarization)
	if len(chunk.Speakers) > 0 {
		utterance.UserID = chunk.Speakers[0]
		utterance.UserTag = chunk.Speakers[0] // Will be resolved to username later
	}

	log.Debug().
		Str("chunk_id", chunk.ID.String()).
		Str("text", utterance.Text).
		Float64("confidence", utterance.Confidence).
		Msg("Vosk transcription completed")

	return []audio.Utterance{utterance}, nil
}

func (v *VoskTranscriber) Close() error {
	if v.recognizer != nil {
		v.recognizer.Free()
	}
	if v.model != nil {
		v.model.Free()
	}
	return nil
}