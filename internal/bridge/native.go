package bridge

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
	request "github.com/freequasar/quasar-go-genproto/proto/alice/protos/api/alicekit/events/alice_request"
	response "github.com/freequasar/quasar-go-genproto/proto/alice/protos/api/alicekit/events/alice_response"
	cancellation "github.com/freequasar/quasar-go-genproto/proto/alice/protos/api/alicekit/events/cancel_request"
	header "github.com/freequasar/quasar-go-genproto/proto/alice/protos/api/alicekit/events/common/event_header"
	continuation "github.com/freequasar/quasar-go-genproto/proto/alice/protos/api/alicekit/events/continue_request"
	ack "github.com/freequasar/quasar-go-genproto/proto/alice/protos/api/alicekit/events/input_start_ack"
	tts "github.com/freequasar/quasar-go-genproto/proto/alice/protos/api/alicekit/events/tts_generate"
	client "github.com/freequasar/quasar-go-genproto/proto/alice/protos/api/alicekit/protocol/client"
	server "github.com/freequasar/quasar-go-genproto/proto/alice/protos/api/alicekit/protocol/server"
	"google.golang.org/protobuf/proto"

	"stationhermes/internal/wire"
)

type Resolver interface {
	ResolveStream(context.Context, string, func(string)) (string, error)
}

type nativeRequest struct {
	streaming                       bool
	partial                         uint32
	responseID                      string
	final                           bool
	header                          *header.TEventHeader
	ttsStarted                      bool
	gated                           bool
	input                           []byte
	id                              string
	request                         *request.TAliceRequestProto
	text                            string
	ctx                             context.Context
	cancel                          context.CancelFunc
	started, canceled, local, cloud bool
	continuation                    []byte
	ttsID                           string
	progressTTS                     string
	audio                           oggSpeech
	audioIsProgress                 bool
	audioStarted                    bool
	audioStream                     uint32
	audioWaiting                    bool
	progressAudioDone               chan struct{}
	speechMu                        sync.Mutex
	streamedText                    string
	intermediateTTS                 map[string]chan struct{}
	streamDisabled                  bool
	expires                         *time.Timer
}
type Native struct {
	ctx      context.Context
	cancel   context.CancelFunc
	up, down Sender
	resolver Resolver
	mu       sync.Mutex
	requests map[string]*nativeRequest
	tts      map[string]*nativeRequest
	streams  map[uint32]*nativeRequest
	blocked  map[uint32]*nativeRequest
	wg       sync.WaitGroup
}

func NewNative(ctx context.Context, up, down Sender, resolver Resolver) *Native {
	ctx, cancel := context.WithCancel(ctx)
	return &Native{ctx: ctx, cancel: cancel, up: up, down: down, resolver: resolver, requests: map[string]*nativeRequest{}, tts: map[string]*nativeRequest{}, streams: map[uint32]*nativeRequest{}, blocked: map[uint32]*nativeRequest{}}
}
func (n *Native) Close() {
	n.cancel()
	n.mu.Lock()
	for _, p := range n.requests {
		p.canceled = true
		p.cancel()
		p.expires.Stop()
	}
	n.mu.Unlock()
	n.wg.Wait()
}
func (n *Native) Client(ctx context.Context, k websocket.MessageType, data []byte) error {
	f := wire.Decode(data, wire.ClientToServer)
	if f.Client == nil || f.Client.GetEvent() == nil {
		return n.up(ctx, k, data)
	}
	e := f.Client.GetEvent()

	var req *request.TAliceRequestProto
	switch f.Kind() {
	case "TextInput":
		req = e.GetTextInput()
	case "VoiceInput":
		req = e.GetVoiceInput()
	case "SpeechInput":
		req = e.GetSpeechInput()
	}
	if req != nil && f.Kind() == "TextInput" && strings.TrimSpace(req.GetRequest().GetEvent().GetText()) == "" {
		return n.up(ctx, k, data)
	}
	if req != nil {
		id := e.GetHeader().GetMessageId()
		if id == "" {
			return n.up(ctx, k, data)
		}
		pctx, cancel := context.WithTimeout(n.ctx, 90*time.Second)
		p := &nativeRequest{id: id, request: proto.Clone(req).(*request.TAliceRequestProto), text: req.GetRequest().GetEvent().GetText(), ctx: pctx, cancel: cancel}
		p.header = proto.Clone(e.GetHeader()).(*header.TEventHeader)
		p.input = append([]byte{}, data...)
		p.gated = f.Kind() != "TextInput"

		slog.Info("native_request_shape", "request", requestTag(id), "text_bytes", len(p.text), "event_type", req.GetRequest().GetEvent().GetType().String(), "stream", e.GetHeader().GetStreamId(), "unknown_request_bytes", len(req.ProtoReflect().GetUnknown()), "payload_bytes", len(req.GetRequest().GetEvent().GetPayloadRaw()))
		n.mu.Lock()
		if n.requests[id] != nil {
			n.mu.Unlock()
			cancel()
			return nil
		}
		if len(n.requests) >= 128 {
			n.mu.Unlock()
			cancel()
			return errors.New("too many pending native requests")
		}
		p.expires = time.AfterFunc(90*time.Second, func() {
			n.mu.Lock()
			defer n.mu.Unlock()
			p.canceled = true
			p.cancel()
			delete(n.requests, p.id)
			delete(n.tts, p.ttsID)
			delete(n.tts, p.progressTTS)
			for stream, q := range n.blocked {
				if q == p {
					delete(n.blocked, stream)
				}
			}
			for stream, q := range n.streams {
				if q == p {
					delete(n.streams, stream)
				}
			}
		})
		n.requests[id] = p
		n.mu.Unlock()
		if p.gated {
			gated, err := wire.EnableApproval(f)
			if err != nil {
				return err
			}
			if err := n.up(ctx, k, gated); err != nil {
				return err
			}
		}
		// The original input stays local until Hermes has produced its answer.
		n.maybeResolve(p)
		return nil
	}
	if c := e.GetContinueRequest(); c != nil {
		n.mu.Lock()
		p := n.requests[c.GetRequestMessageId()]
		if p != nil && (p.canceled || p.local) {
			n.mu.Unlock()
			return nil
		}
		if p != nil && !p.cloud {
			if p.continuation != nil {
				n.mu.Unlock()
				return nil
			}
			p.continuation = append([]byte{}, data...)
			n.mu.Unlock()
			n.maybeResolve(p)
			return nil
		}
		n.mu.Unlock()
	}
	if c := e.GetCancelRequest(); c != nil {
		n.mu.Lock()
		p := n.requests[c.GetRequestMessageId()]
		ttsID := ""
		if p != nil {
			p.canceled = true
			p.cancel()
			ttsID = p.ttsID
		}
		n.mu.Unlock()
		if ttsID != "" {
			_ = n.sendCancel(ctx, ttsID)
		}
	}
	return n.up(ctx, k, data)
}
func (n *Native) findLocked(h *header.TEventHeader) (*nativeRequest, bool) {
	if h == nil {
		return nil, false
	}
	for _, id := range []string{h.GetRefMessageId(), h.GetSubRequestRef().GetMessageId(), h.GetSubRequestRef().GetRequestId()} {
		if p := n.tts[id]; p != nil {
			return p, true
		}
		if p := n.requests[id]; p != nil {
			return p, false
		}
		if id != "" {
			for _, p := range n.requests {
				if p.request.GetHeader().GetRequestId() == id {
					return p, false
				}
			}
		}
	}
	return nil, false
}
func (n *Native) Server(ctx context.Context, k websocket.MessageType, data []byte) error {
	f := wire.Decode(data, wire.ServerToClient)
	if f.Server == nil {
		if len(data) >= 4 {
			n.mu.Lock()
			p := n.streams[binary.BigEndian.Uint32(data)]
			drop := n.blocked[binary.BigEndian.Uint32(data)] != nil || (p != nil && p.canceled)
			if p != nil && p.streaming && !drop {
				out, err := p.audio.feed(data[4:])
				if err == nil && len(out) > 0 {
					err = n.down(ctx, k, append(binary.BigEndian.AppendUint32(nil, p.audioStream), out...))
				}
				if err != nil {
					p.cancel()
				}
				n.mu.Unlock()
				return err
			}
			n.mu.Unlock()
			if drop {
				return nil
			}
		}
		return n.down(ctx, k, data)
	}
	if sc := f.Server.GetStreamControl(); sc != nil {
		n.mu.Lock()
		if n.blocked[uint32(sc.GetStreamId())] != nil {
			delete(n.blocked, uint32(sc.GetStreamId()))
			n.mu.Unlock()
			return nil
		}
		p := n.tts[sc.GetMessageId()]
		if p == nil {
			p = n.streams[uint32(sc.GetStreamId())]
		}
		if sc.GetClose() != nil {
			delete(n.streams, uint32(sc.GetStreamId()))
		}
		original := n.requests[sc.GetMessageId()]
		if p != nil {
			if p.canceled {
				n.mu.Unlock()
				return nil
			}
			if p.streaming && sc.GetClose() != nil {
				if err := p.audio.endSource(); err != nil {
					p.cancel()
					n.mu.Unlock()
					return err
				}
				if p.audioIsProgress && sc.GetClose().GetReason() == 0 {
					if tail := p.audio.flushPending(0, 0); len(tail) > 0 {
						if err := n.down(ctx, k, append(binary.BigEndian.AppendUint32(nil, p.audioStream), tail...)); err != nil {
							n.mu.Unlock()
							return err
						}
					}
					if !p.audioWaiting {
						p.audioWaiting = true
						n.waitAudio(p)
						close(p.progressAudioDone)
					}
					n.mu.Unlock()
					return nil
				}
				if done := p.intermediateTTS[sc.GetMessageId()]; done != nil && sc.GetClose().GetReason() == 0 {
					delete(p.intermediateTTS, sc.GetMessageId())
					if tail := p.audio.flushPending(0, 0); len(tail) > 0 {
						if err := n.down(ctx, k, append(binary.BigEndian.AppendUint32(nil, p.audioStream), tail...)); err != nil {
							n.mu.Unlock()
							return err
						}
					}
					if !p.audioWaiting {
						p.audioWaiting = true
						n.waitAudio(p)
					}
					close(done)
					n.mu.Unlock()
					return nil
				}
				end := p.audio.finish()
				if err := n.down(ctx, k, append(binary.BigEndian.AppendUint32(nil, p.audioStream), end...)); err != nil {
					n.mu.Unlock()
					return err
				}
				sc.StreamId = int64(p.audioStream)
			}
			sc.MessageId = p.id
			n.mu.Unlock()
			out, err := f.Marshal()
			if err != nil {
				return err
			}
			return n.down(ctx, k, out)
		}
		drop := original != nil && original.local && (original.header.StreamId == nil || sc.GetStreamId() != original.header.GetStreamId())
		n.mu.Unlock()
		if drop {
			return nil
		}
		return n.down(ctx, k, data)
	}
	e := f.Server.GetEvent()
	if e == nil {
		return n.down(ctx, k, data)
	}

	n.mu.Lock()
	if f.Kind() == "TtsSpeak" {
		delete(n.streams, uint32(e.GetHeader().GetStreamId()))
		delete(n.blocked, uint32(e.GetHeader().GetStreamId()))
	}
	p, isTTS := n.findLocked(e.GetHeader())
	if r := e.GetAliceResponse(); r != nil {
		var ds []string
		for _, d := range r.GetResponse().GetDirectives() {
			ds = append(ds, d.GetType()+":"+d.GetName())
		}

		slog.Info("native_cloud_shape", "matched", p != nil, "directives", ds, "cards", len(r.GetResponse().GetCards()), "voice_text_bytes", len(r.GetVoiceResponse().GetOutputSpeech().GetText()))
	}
	if p == nil {
		n.mu.Unlock()
		return n.down(ctx, k, data)
	}
	if isTTS {
		if p.canceled {
			n.mu.Unlock()
			return nil
		}
		e.Header.RefMessageId = p.id
		if ref := e.Header.SubRequestRef; ref != nil {
			ref.MessageId = p.id
			ref.RequestId = p.request.GetHeader().GetRequestId()
		}
		if e.GetTtsSpeak() != nil {
			n.streams[uint32(e.Header.GetStreamId())] = p
			if p.streaming {
				if e.GetTtsSpeak().GetFormat() != "audio/opus" {
					p.cancel()
					n.mu.Unlock()
					return errors.New("streaming progress requires Opus")
				}
				p.audioIsProgress = p.ttsID == p.progressTTS
				p.audio.next()
				// The initial progress segment is the one-shot spoken acknowledgement;
				// keep its Opus packets in the same deferred stream as the final reply.
				p.audio.source.discardAudio = false
				if p.audioStarted {
					p.audioWaiting = false
					n.mu.Unlock()
					return nil
				}
				p.audioStream = uint32(e.Header.GetStreamId())
				p.audioStarted = true
			}
			slog.Info("native_tts_ready", "request", requestTag(p.id), "format", e.GetTtsSpeak().GetFormat(), "stream", e.Header.GetStreamId())
		}
		n.mu.Unlock()
		out, err := f.Marshal()
		if err != nil {
			return err
		}
		return n.down(ctx, k, out)
	}

	if f.Kind() == "TtsSpeak" {
		p.ttsStarted = true
		if p.local || p.canceled {
			n.blocked[uint32(e.GetHeader().GetStreamId())] = p
		}
	}
	if p.local || p.canceled {
		n.mu.Unlock()
		return nil
	}

	switch f.Kind() {
	case "AliceResponse":
		if !p.cloud {
			p.canceled = true
			p.cancel()
			slog.Warn("native_approval_not_honored", "request", requestTag(p.id))
		}
	case "ServerRequestReady":
		slog.Info("native_server_ready", "request", requestTag(p.id))
	case "AsrResult":
		asr := e.GetAsrResult()
		if asr.GetEndOfUtt() {
			p.final = true
			slog.Info("native_asr_final", "request", requestTag(p.id), "hypotheses", len(asr.GetRecognition()), "code", asr.GetResponseCode().String())
		}
		if asr.GetEndOfUtt() && len(asr.GetRecognition()) > 0 {
			r := asr.GetRecognition()[0]
			p.text = r.GetNormalized()
			if p.text == "" {
				var words []string
				for _, w := range r.GetWords() {
					words = append(words, w.GetValue())
				}
				p.text = strings.Join(words, " ")
			}
		}

	}
	n.mu.Unlock()
	n.maybeResolve(p)
	if err := n.down(ctx, k, data); err != nil {
		return err
	}
	return nil
}
func (n *Native) maybeResolve(p *nativeRequest) {
	n.mu.Lock()
	if p.started || p.canceled || (strings.TrimSpace(p.text) == "" && !p.final) {
		n.mu.Unlock()
		return
	}
	p.started = true
	text := p.text
	n.mu.Unlock()
	n.wg.Go(func() {

		var answer string
		useHermes := n.resolver != nil && strings.TrimSpace(text) != ""
		var err error
		if useHermes {
			stop := n.progress(p)
			answer, err = n.resolver.ResolveStream(p.ctx, text, func(sentence string) { n.streamSentence(p, sentence) })
			stop()
		}

		if p.ctx.Err() != nil {
			return
		}
		if errors.Is(err, context.Canceled) {
			n.discardStale(p)
			return
		}
		if err != nil {
			slog.Warn("native_hermes_request_failed", "request", requestTag(p.id), "error", err)
			answer = "Сейчас не получилось получить ответ Hermes."
		}
		n.mu.Lock()
		if p.canceled {
			n.mu.Unlock()
			return
		}
		if !useHermes {
			p.cloud = true
			var err error
			if p.gated {
				control := p.continuation
				if control == nil {
					control, err = n.makeContinue(p)
				}
				if err == nil {
					err = n.up(p.ctx, websocket.MessageBinary, control)
				}
			} else {
				err = n.up(p.ctx, websocket.MessageBinary, p.input)
				if err == nil && p.continuation != nil {
					err = n.up(p.ctx, websocket.MessageBinary, p.continuation)
				}
			}
			n.mu.Unlock()
			slog.Info("native_route", "request", requestTag(p.id), "route", "yandex", "sent", err == nil)
			return
		}
		p.local = true
		if !p.streaming {
			p.ttsID = newID()
			n.tts[p.ttsID] = p
		}
		n.mu.Unlock()
		slog.Info("native_route", "request", requestTag(p.id), "route", "hermes")
		if p.gated && !p.streaming {
			if err := n.sendCancel(p.ctx, p.id); err != nil {
				return
			}
		}
		if err := n.reply(p, answer); err != nil {
			slog.Warn("native_reply_failed", "request", requestTag(p.id))
		}
	})
}
func (n *Native) sendCancel(ctx context.Context, id string) error {
	m := &client.TClientMessage{Message: &client.TClientMessage_Event{Event: &client.TClientMessage_TEvent{Header: &header.TEventHeader{MessageId: newID()}, Event: &client.TClientMessage_TEvent_CancelRequest{CancelRequest: &cancellation.TCancelRequest{RequestMessageId: id, Reason: &cancellation.TCancelRequest_LocalExecution{LocalExecution: &cancellation.TCancelRequest_TReasonLocalExecution{}}}}}}}
	b, err := (wire.Frame{Client: m, Prefix: []byte("AAPI")}).Marshal()
	if err != nil {
		return err
	}
	return n.up(ctx, websocket.MessageBinary, b)
}
func (n *Native) reply(p *nativeRequest, text string) error {
	if p.streaming {
		return n.finishDeferred(p, text)
	}
	requestID := p.request.GetHeader().GetRequestId()
	if requestID == "" {
		requestID = p.id
	}
	answer := &response.TAliceResponseProto{Header: &response.TAliceResponseProto_THeader{RequestId: new(requestID), ResponseId: new(newID()), DialogId: new(p.request.GetHeader().GetDialogId()), SequenceNumber: new(p.request.GetHeader().GetSequenceNumber())}, VoiceResponse: &response.TAliceResponseProto_TVoiceResponse{OutputSpeech: &response.TAliceResponseProto_TVoiceResponse_TOutputSpeech{Text: new(text)}, HasVoiceResponse: true}, Response: &response.TAliceResponseProto_TResponse{Cards: []*response.TAliceResponseProto_TResponse_TCard{{Type: "simple_text", Text: new(text), CardId: new(newID())}}}}
	answer.VoiceResponse.ShouldListen = true
	m := &server.TServerMessage{Message: &server.TServerMessage_Event{Event: &server.TServerMessage_TEvent{Header: &header.TEventHeader{MessageId: newID(), RefMessageId: p.id}, Event: &server.TServerMessage_TEvent_AliceResponse{AliceResponse: answer}}}}
	b, err := (wire.Frame{Server: m, Prefix: []byte("AAPI")}).Marshal()
	if err != nil {
		return err
	}
	if !p.gated && !p.cloud {
		if err := n.ack(p); err != nil {
			return err
		}
	}
	if err := n.down(p.ctx, websocket.MessageBinary, b); err != nil {
		return err
	}
	return n.synthesize(p, text)
}

// streamSentence starts a new TTS segment inside the deferred audio stream. It
// only streams when the long-turn path has opened; quick replies stay on the
// ordinary single-response path.
func (n *Native) streamSentence(p *nativeRequest, sentence string) {
	sentence = strings.TrimSpace(sentence)
	if sentence == "" {
		return
	}
	p.speechMu.Lock()
	defer p.speechMu.Unlock()
	n.mu.Lock()
	if !p.streaming && !p.canceled {
		// An early sentence that arrives before the delayed stream envelope must
		// remain part of the final reply; don't later stream a non-prefix sentence.
		p.streamDisabled = true
	}
	ready := p.streaming && !p.canceled && !p.streamDisabled && p.progressAudioDone != nil
	ackDone := p.progressAudioDone
	n.mu.Unlock()
	if !ready {
		return
	}
	select {
	case <-p.ctx.Done():
		return
	case <-ackDone:
	}
	n.mu.Lock()
	if !p.streaming || p.canceled {
		n.mu.Unlock()
		return
	}
	ttsID := newID()
	done := make(chan struct{})
	if p.intermediateTTS == nil {
		p.intermediateTTS = make(map[string]chan struct{})
	}
	p.intermediateTTS[ttsID] = done
	p.ttsID = ttsID
	n.tts[ttsID] = p
	n.mu.Unlock()
	if err := n.deferredPart(p, sentence, false); err != nil {
		p.cancel()
		return
	}
	if err := n.synthesize(p, sentence); err != nil {
		p.cancel()
		return
	}
	select {
	case <-p.ctx.Done():
		return
	case <-done:
	}
	p.streamedText += sentence + " "
}

func (n *Native) synthesize(p *nativeRequest, text string) error {
	t := &client.TClientMessage{Message: &client.TClientMessage_Event{Event: &client.TClientMessage_TEvent{Header: &header.TEventHeader{MessageId: p.ttsID}, Event: &client.TClientMessage_TEvent_TtsGenerate{TtsGenerate: &tts.TTtsGenerate{Text: new(text), Lang: new("ru-RU"), Format: new("audio/opus")}}}}}
	b, err := (wire.Frame{Client: t, Prefix: []byte("AAPI")}).Marshal()
	if err != nil {
		return err
	}
	return n.up(p.ctx, websocket.MessageBinary, b)
}
func requestTag(id string) string {
	sum := sha256.Sum256([]byte(id))
	return fmt.Sprintf("%x", sum[:4])
}

// Quasar uses Yandex TGUID spelling (8-8-8-8), not RFC UUID spelling.
func newID() string {
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		panic("cannot create protocol message ID")
	}
	return fmt.Sprintf("%x-%x-%x-%x", id[0:4], id[4:8], id[8:12], id[12:16])
}

func (n *Native) makeContinue(p *nativeRequest) ([]byte, error) {
	h := proto.Clone(p.header).(*header.TEventHeader)
	h.MessageId = newID()
	h.RefMessageId = ""
	h.StreamId = nil
	m := &client.TClientMessage{Message: &client.TClientMessage_Event{Event: &client.TClientMessage_TEvent{Header: h, Event: &client.TClientMessage_TEvent_ContinueRequest{ContinueRequest: &continuation.TContinueRequest{RequestMessageId: p.id}}}}}
	return (wire.Frame{Client: m, Prefix: []byte("AAPI")}).Marshal()
}

func (n *Native) ack(p *nativeRequest) error {
	m := &server.TServerMessage{Message: &server.TServerMessage_Event{Event: &server.TServerMessage_TEvent{Header: &header.TEventHeader{MessageId: newID(), RefMessageId: p.id}, Event: &server.TServerMessage_TEvent_InputStartAck{InputStartAck: &ack.TInputStartAck{RequestStartTime: uint64(time.Now().UnixMilli())}}}}}
	b, err := (wire.Frame{Server: m, Prefix: []byte("AAPI")}).Marshal()
	if err != nil {
		return err
	}
	return n.down(p.ctx, websocket.MessageBinary, b)
}

func (n *Native) discardStale(p *nativeRequest) {
	n.mu.Lock()
	p.canceled = true
	p.cancel()
	n.mu.Unlock()
	_ = n.sendCancel(n.ctx, p.id)
}
