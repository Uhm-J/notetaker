package audio

import (
	"time"

	"github.com/google/uuid"
)

// Chunk represents a segment of audio data for processing
type Chunk struct {
	ID       uuid.UUID
	PCM      []int16
	Start    time.Time
	End      time.Time
	Speakers []string // Discord user IDs of speakers in this chunk
}

// Utterance represents a transcribed piece of speech
type Utterance struct {
	ID       uuid.UUID     `json:"id"`
	TSStart  time.Time     `json:"ts_start"`
	TSEnd    time.Time     `json:"ts_end"`
	UserID   string        `json:"user_id"`
	UserTag  string        `json:"user_tag"`
	Text     string        `json:"text"`
	Source   string        `json:"source"` // "vosk" or "deepgram"
	Confidence float64     `json:"confidence,omitempty"`
}

// VoiceState tracks Discord voice state for a user
type VoiceState struct {
	UserID    string
	Username  string
	Speaking  bool
	Timestamp time.Time
}

// AudioDecoder interface for different audio decoders
type AudioDecoder interface {
	Decode(opus []byte) ([]int16, error)
}

// VAD interface for Voice Activity Detection
type VAD interface {
	IsSpeech(pcm []int16, sampleRate int) bool
	Close() error
}

// Chunker interface for audio chunking
type Chunker interface {
	AddSamples(pcm []int16, timestamp time.Time, speakers []string)
	GetChunk() <-chan *Chunk
	Stop()
}