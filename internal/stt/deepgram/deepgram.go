package deepgram

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"

	"github.com/google/uuid"
	"github.com/rs/zerolog/log"
	"github.com/user/discord-notetaker/internal/audio"
)

type DeepgramTranscriber struct {
	apiKey     string
	model      string
	diarize    bool
	punctuate  bool
	utterances bool
}

type DeepgramResponse struct {
	Results struct {
		Channels []struct {
			Alternatives []struct {
				Transcript string  `json:"transcript"`
				Confidence float64 `json:"confidence"`
			} `json:"alternatives"`
		} `json:"channels"`
	} `json:"results"`
}

func NewDeepgramTranscriber(apiKey, model string, diarize, punctuate, utterances bool) *DeepgramTranscriber {
	return &DeepgramTranscriber{
		apiKey:     apiKey,
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

	// Build URL with query parameters according to API documentation
	baseURL := "https://api.deepgram.com/v1/listen"
	params := url.Values{}

	// Add parameters based on API documentation
	if d.model != "" {
		params.Set("model", d.model)
	}
	params.Set("punctuate", strconv.FormatBool(d.punctuate))
	params.Set("diarize", strconv.FormatBool(d.diarize))
	params.Set("utterances", strconv.FormatBool(d.utterances))
	params.Set("smart_format", "true")
	params.Set("language", "en")

	// Build final URL
	fullURL := baseURL + "?" + params.Encode()

	log.Debug().
		Str("url", fullURL).
		Str("model", d.model).
		Bool("punctuate", d.punctuate).
		Bool("diarize", d.diarize).
		Bool("utterances", d.utterances).
		Int("audio_size_bytes", len(wavData)).
		Msg("Making Deepgram API request")

	// Create HTTP request
	req, err := http.NewRequestWithContext(ctx, "POST", fullURL, bytes.NewReader(wavData))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	// Set headers according to API documentation
	req.Header.Set("Authorization", "Token "+d.apiKey)
	req.Header.Set("Content-Type", "audio/*") // As specified in the API docs

	// Make request
	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("Deepgram API request failed: %w", err)
	}
	defer resp.Body.Close()

	// Read response body for debugging
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response body: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		log.Warn().
			Int("status_code", resp.StatusCode).
			Str("response_body", string(body)).
			Str("url", fullURL).
			Msg("Deepgram API error response")
		return nil, fmt.Errorf("Deepgram API error %d: %s", resp.StatusCode, string(body))
	}

	// Parse response
	var result DeepgramResponse
	if err := json.Unmarshal(body, &result); err != nil {
		log.Warn().
			Str("response_body", string(body)).
			Msg("Failed to parse Deepgram response")
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}

	if len(result.Results.Channels) == 0 {
		log.Debug().Msg("No channels in Deepgram response")
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

		log.Debug().
			Str("transcript", alternative.Transcript).
			Float64("confidence", alternative.Confidence).
			Msg("Received transcription")
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
	// HTTP client doesn't require explicit cleanup
	return nil
}
