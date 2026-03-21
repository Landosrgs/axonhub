package orchestrator

import (
	"context"
	"regexp"
	"strings"
	"sync"

	"github.com/looplj/axonhub/internal/log"
	"github.com/looplj/axonhub/internal/objects"
	"github.com/looplj/axonhub/llm"
	"github.com/looplj/axonhub/llm/pipeline"
	"github.com/looplj/axonhub/llm/streams"
)

// applyRegexFilter creates a middleware that filters response content using regex rules
// configured in channel settings.
func applyRegexFilter(outbound *PersistentOutboundTransformer) pipeline.Middleware {
	return &regexFilterMiddleware{
		outbound: outbound,
	}
}

type regexFilterMiddleware struct {
	pipeline.DummyMiddleware
	outbound *PersistentOutboundTransformer
}

func (m *regexFilterMiddleware) Name() string {
	return "regex-filter"
}

// OnOutboundLlmResponse filters content in non-streaming responses.
func (m *regexFilterMiddleware) OnOutboundLlmResponse(ctx context.Context, response *llm.Response) (*llm.Response, error) {
	rules := m.getEnabledRules()
	if len(rules) == 0 {
		return response, nil
	}

	filters, err := compileFilterRules(rules)
	if err != nil {
		log.Warn(ctx, "failed to compile regex filter rules, skipping filter", log.Cause(err))
		return response, nil
	}

	for i := range response.Choices {
		choice := &response.Choices[i]
		if choice.Message != nil {
			filterMessageContent(choice.Message, filters)
		}
	}

	return response, nil
}

// OnOutboundLlmStream wraps the stream to filter content chunk by chunk.
func (m *regexFilterMiddleware) OnOutboundLlmStream(ctx context.Context, stream streams.Stream[*llm.Response]) (streams.Stream[*llm.Response], error) {
	rules := m.getEnabledRules()
	if len(rules) == 0 {
		return stream, nil
	}

	filters, err := compileFilterRules(rules)
	if err != nil {
		log.Warn(ctx, "failed to compile regex filter rules, skipping filter", log.Cause(err))
		return stream, nil
	}

	hasRegexRules := false
	for _, f := range filters {
		if f.mode == objects.RegexFilterModeRegex {
			hasRegexRules = true
			break
		}
	}

	if hasRegexRules {
		// Pseudo-streaming: buffer all chunks, filter, then replay.
		return newPseudoStreamFilter(stream, filters), nil
	}

	// True streaming with anchor rules only.
	return newAnchorStreamFilter(stream, filters), nil
}

func (m *regexFilterMiddleware) getEnabledRules() []objects.RegexFilterRule {
	channel := m.outbound.GetCurrentChannel()
	if channel == nil || channel.Settings == nil {
		return nil
	}

	var rules []objects.RegexFilterRule
	for _, rule := range channel.Settings.RegexFilters {
		if rule.Enabled && rule.Pattern != "" {
			rules = append(rules, rule)
		}
	}

	return rules
}

// compiledFilter holds a compiled regex or anchor pattern.
type compiledFilter struct {
	pattern *regexp.Regexp // non-nil for regex mode
	anchor  string         // non-empty for anchor mode
	mode    objects.RegexFilterMode
}

func compileFilterRules(rules []objects.RegexFilterRule) ([]compiledFilter, error) {
	filters := make([]compiledFilter, 0, len(rules))
	for _, rule := range rules {
		switch rule.Mode {
		case objects.RegexFilterModeAnchor:
			filters = append(filters, compiledFilter{
				anchor: rule.Pattern,
				mode:   rule.Mode,
			})
		default: // regex
			compiled, err := regexp.Compile(rule.Pattern)
			if err != nil {
				return nil, err
			}
			filters = append(filters, compiledFilter{
				pattern: compiled,
				mode:    rule.Mode,
			})
		}
	}

	return filters, nil
}

// filterMessageContent applies all filter rules to a message's content fields.
func filterMessageContent(msg *llm.Message, filters []compiledFilter) {
	if msg.Content.Content != nil {
		filtered := applyFilters(*msg.Content.Content, filters)
		msg.Content.Content = &filtered
	}

	if len(msg.Content.MultipleContent) > 0 {
		for i := range msg.Content.MultipleContent {
			part := &msg.Content.MultipleContent[i]
			if part.Text != nil {
				filtered := applyFilters(*part.Text, filters)
				part.Text = &filtered
			}
		}
	}

	if msg.ReasoningContent != nil {
		filtered := applyFilters(*msg.ReasoningContent, filters)
		msg.ReasoningContent = &filtered
	}
}

// applyFilters applies all filter rules to a string.
func applyFilters(content string, filters []compiledFilter) string {
	for _, f := range filters {
		switch f.mode {
		case objects.RegexFilterModeAnchor:
			if idx := strings.Index(content, f.anchor); idx >= 0 {
				content = content[:idx]
			}
		default: // regex
			content = f.pattern.ReplaceAllString(content, "")
		}
	}

	return content
}

// =============================================================================
// Anchor-mode true streaming filter
// =============================================================================

// anchorStreamFilter filters a stream in real-time using anchor rules.
// When an anchor is detected, content from that point is truncated and
// subsequent chunks are discarded.
type anchorStreamFilter struct {
	source  streams.Stream[*llm.Response]
	filters []compiledFilter
	current *llm.Response
	err     error

	// tailBuf holds trailing bytes from the previous chunk that could be
	// the start of an anchor match spanning two chunks.
	tailBuf   string
	truncated bool // once true, all subsequent content is discarded
}

func newAnchorStreamFilter(source streams.Stream[*llm.Response], filters []compiledFilter) *anchorStreamFilter {
	return &anchorStreamFilter{
		source:  source,
		filters: filters,
	}
}

func (s *anchorStreamFilter) Next() bool {
	for s.source.Next() {
		event := s.source.Current()
		if event == nil {
			continue
		}

		// Pass through non-content events (usage, [DONE], etc.)
		if len(event.Choices) == 0 {
			s.current = event
			return true
		}

		if s.truncated {
			// Already truncated: pass through finish events but drop content.
			hasFinish := false
			for _, c := range event.Choices {
				if c.FinishReason != nil {
					hasFinish = true
					break
				}
			}
			if hasFinish {
				// Clear content but keep finish reason and usage.
				filtered := s.clearContent(event)
				s.current = filtered
				return true
			}
			// Drop pure content chunks.
			continue
		}

		// Process content chunks.
		filtered, shouldTruncate := s.processChunk(event)
		if shouldTruncate {
			s.truncated = true
		}

		if filtered != nil {
			s.current = filtered
			return true
		}
		// filtered == nil means the entire chunk was consumed by the tail buffer;
		// continue to the next chunk.
	}

	// Stream ended. If there's anything left in tailBuf, emit it.
	if s.tailBuf != "" {
		s.current = s.emitTailBuf()
		s.tailBuf = ""
		return true
	}

	return false
}

func (s *anchorStreamFilter) Current() *llm.Response {
	return s.current
}

func (s *anchorStreamFilter) Err() error {
	if s.err != nil {
		return s.err
	}
	return s.source.Err()
}

func (s *anchorStreamFilter) Close() error {
	return s.source.Close()
}

// processChunk checks a response chunk for anchor matches.
// Returns the (possibly modified) chunk and whether truncation was triggered.
func (s *anchorStreamFilter) processChunk(event *llm.Response) (*llm.Response, bool) {
	// Extract delta text from the first choice.
	deltaText := getDeltaText(event)
	if deltaText == "" {
		return event, false
	}

	// Prepend any buffered tail from the previous chunk.
	combined := s.tailBuf + deltaText
	s.tailBuf = ""

	// Check each anchor filter.
	for _, f := range s.filters {
		if f.mode != objects.RegexFilterModeAnchor {
			continue
		}
		if idx := strings.Index(combined, f.anchor); idx >= 0 {
			// Truncate at anchor position.
			safe := combined[:idx]
			if safe == "" {
				return nil, true
			}
			setDeltaText(event, safe)
			return event, true
		}
	}

	// Check if the tail of combined could be the start of an anchor.
	maxAnchorLen := s.maxAnchorLen()
	if maxAnchorLen > 0 && len(combined) > 0 {
		// Keep up to maxAnchorLen-1 chars as tail buffer.
		bufLen := maxAnchorLen - 1
		if bufLen > len(combined) {
			bufLen = len(combined)
		}
		// Check if any suffix of combined could be a prefix of any anchor.
		tail := combined[len(combined)-bufLen:]
		actualBuf := 0
		for i := 0; i < len(tail); i++ {
			suffix := tail[i:]
			for _, f := range s.filters {
				if f.mode == objects.RegexFilterModeAnchor && strings.HasPrefix(f.anchor, suffix) {
					remaining := len(tail) - i
					if remaining > actualBuf {
						actualBuf = remaining
					}
				}
			}
		}
		if actualBuf > 0 {
			s.tailBuf = combined[len(combined)-actualBuf:]
			safe := combined[:len(combined)-actualBuf]
			if safe == "" {
				return nil, false
			}
			setDeltaText(event, safe)
			return event, false
		}
	}

	// No anchor concern, emit everything.
	setDeltaText(event, combined)
	return event, false
}

func (s *anchorStreamFilter) maxAnchorLen() int {
	maxLen := 0
	for _, f := range s.filters {
		if f.mode == objects.RegexFilterModeAnchor && len(f.anchor) > maxLen {
			maxLen = len(f.anchor)
		}
	}
	return maxLen
}

func (s *anchorStreamFilter) emitTailBuf() *llm.Response {
	text := s.tailBuf
	return &llm.Response{
		Choices: []llm.Choice{
			{
				Delta: &llm.Message{
					Content: llm.MessageContent{Content: &text},
				},
			},
		},
	}
}

func (s *anchorStreamFilter) clearContent(event *llm.Response) *llm.Response {
	clone := *event
	clone.Choices = make([]llm.Choice, len(event.Choices))
	for i, c := range event.Choices {
		clone.Choices[i] = c
		if c.Delta != nil {
			emptyDelta := *c.Delta
			emptyStr := ""
			emptyDelta.Content = llm.MessageContent{Content: &emptyStr}
			emptyDelta.ReasoningContent = nil
			clone.Choices[i].Delta = &emptyDelta
		}
	}
	return &clone
}

// =============================================================================
// Pseudo-streaming filter (for regex mode)
// =============================================================================

// pseudoStreamFilter buffers all stream chunks, then applies regex filters
// and replays the filtered content.
type pseudoStreamFilter struct {
	source  streams.Stream[*llm.Response]
	filters []compiledFilter

	mu       sync.Mutex
	buffered []*llm.Response
	replayed bool
	replayIdx int
	current  *llm.Response
	err      error
}

func newPseudoStreamFilter(source streams.Stream[*llm.Response], filters []compiledFilter) *pseudoStreamFilter {
	return &pseudoStreamFilter{
		source:  source,
		filters: filters,
	}
}

func (s *pseudoStreamFilter) Next() bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	if !s.replayed {
		// Buffer all chunks first.
		for s.source.Next() {
			event := s.source.Current()
			if event != nil {
				s.buffered = append(s.buffered, event)
			}
		}
		if err := s.source.Err(); err != nil {
			s.err = err
			return false
		}
		// Apply filters to all buffered content.
		s.applyFiltersToBuffered()
		s.replayed = true
		s.replayIdx = 0
	}

	if s.replayIdx < len(s.buffered) {
		s.current = s.buffered[s.replayIdx]
		s.replayIdx++
		return true
	}

	return false
}

func (s *pseudoStreamFilter) Current() *llm.Response {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.current
}

func (s *pseudoStreamFilter) Err() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return s.err
	}
	return s.source.Err()
}

func (s *pseudoStreamFilter) Close() error {
	return s.source.Close()
}

func (s *pseudoStreamFilter) applyFiltersToBuffered() {
	// Collect all delta text across chunks.
	var sb strings.Builder
	for _, event := range s.buffered {
		sb.WriteString(getDeltaText(event))
	}
	fullText := sb.String()
	if fullText == "" {
		return
	}

	// Apply all filters (both regex and anchor).
	filtered := applyFilters(fullText, s.filters)

	// Redistribute filtered text back to chunks.
	// Strategy: put all filtered text in the first content chunk, clear others.
	placed := false
	for _, event := range s.buffered {
		for i := range event.Choices {
			choice := &event.Choices[i]
			if choice.Delta != nil {
				if !placed {
					choice.Delta.Content = llm.MessageContent{Content: &filtered}
					placed = true
				} else {
					emptyStr := ""
					choice.Delta.Content = llm.MessageContent{Content: &emptyStr}
				}
				choice.Delta.ReasoningContent = nil
			}
		}
	}
}

// =============================================================================
// Helpers
// =============================================================================

// getDeltaText extracts the text content from a streaming response's delta.
func getDeltaText(event *llm.Response) string {
	if event == nil || len(event.Choices) == 0 {
		return ""
	}
	delta := event.Choices[0].Delta
	if delta == nil {
		return ""
	}
	if delta.Content.Content != nil {
		return *delta.Content.Content
	}
	return ""
}

// setDeltaText sets the text content on a streaming response's delta.
func setDeltaText(event *llm.Response, text string) {
	if event == nil || len(event.Choices) == 0 {
		return
	}
	if event.Choices[0].Delta == nil {
		event.Choices[0].Delta = &llm.Message{}
	}
	event.Choices[0].Delta.Content = llm.MessageContent{Content: &text}
}
