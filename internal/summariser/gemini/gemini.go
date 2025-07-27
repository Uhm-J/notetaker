package gemini

import (
	"context"
	"fmt"
	"strings"

	"github.com/google/generative-ai-go/genai"
	"github.com/rs/zerolog/log"
	"github.com/user/discord-notetaker/internal/audio"
	"google.golang.org/api/option"
)

type GeminiSummariser struct {
	client *genai.Client
	model  string
}

func NewGeminiSummariser(apiKey, model string) (*GeminiSummariser, error) {
	ctx := context.Background()
	client, err := genai.NewClient(ctx, option.WithAPIKey(apiKey))
	if err != nil {
		return nil, fmt.Errorf("failed to create Gemini client: %w", err)
	}

	return &GeminiSummariser{
		client: client,
		model:  model,
	}, nil
}

func (g *GeminiSummariser) Summarise(ctx context.Context, utterances []audio.Utterance, mode string) (string, error) {
	if len(utterances) == 0 {
		return "# Meeting Notes\n\nNo transcript available.", nil
	}

	// Convert utterances to transcript text
	transcript := g.buildTranscript(utterances)

	// Generate summary using Gemini
	prompt := g.buildPrompt(transcript, mode)

	genModel := g.client.GenerativeModel(g.model)
	resp, err := genModel.GenerateContent(ctx, genai.Text(prompt))
	if err != nil {
		return "", fmt.Errorf("failed to generate summary: %w", err)
	}

	if len(resp.Candidates) == 0 {
		return "", fmt.Errorf("no summary generated")
	}

	var summary strings.Builder
	for _, part := range resp.Candidates[0].Content.Parts {
		if text, ok := part.(genai.Text); ok {
			summary.WriteString(string(text))
		}
	}

	log.Info().
		Int("utterances", len(utterances)).
		Int("summary_length", summary.Len()).
		Msg("Generated meeting summary")

	return summary.String(), nil
}

func (g *GeminiSummariser) buildTranscript(utterances []audio.Utterance) string {
	var transcript strings.Builder

	for _, utterance := range utterances {
		timestamp := utterance.TSStart.Format("15:04:05")
		speaker := utterance.UserTag
		if speaker == "" {
			speaker = "Unknown"
		}

		transcript.WriteString(fmt.Sprintf("[%s] %s: %s\n",
			timestamp, speaker, utterance.Text))
	}

	return transcript.String()
}

func (g *GeminiSummariser) buildPrompt(transcript, mode string) string {
	var style string
	switch mode {
	case "brief":
		style = "Be extremely concise. Only capture the most important points."
	case "verbose":
		style = "Provide a very detailed summary with as much context as possible."
	case "casual":
		style = "Use a friendly and informal tone in the notes."
	case "formal":
		style = "Use a very formal tone when writing the notes."
	default:
		style = "Be concise but comprehensive."
	}

	return fmt.Sprintf(`You are a meeting notetaker. %s Given a diarized transcript with timestamps, produce:

1) **Summary** - bullet point summary (max 12 bullets)
2) **Decisions** - key decisions made during the meeting
3) **Action Items** - tasks with assignee (if mentioned) and due date (if stated)
4) **Open Questions** - unresolved questions or topics
5) **Key References** - important references with timestamps

Format the output as clean Markdown. %s

**TRANSCRIPT:**
%s

**MEETING NOTES:**`, style, style, transcript)
}

func (g *GeminiSummariser) Close() error {
	if g.client != nil {
		return g.client.Close()
	}
	return nil
}
