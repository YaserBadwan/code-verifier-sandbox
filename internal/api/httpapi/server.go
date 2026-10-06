// Package httpapi exposes the judge as plain JSON over HTTP, using the same messages as the gRPC API.
package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"log/slog"
	"mime"
	"net"
	"net/http"
	"runtime/debug"
	"slices"
	"strconv"
	"strings"
	"time"

	rpccode "google.golang.org/genproto/googleapis/rpc/code"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"

	"github.com/YaserBadwan/code-verifier-sandbox/internal/health"
	"github.com/YaserBadwan/code-verifier-sandbox/internal/judge"
	pb "github.com/YaserBadwan/code-verifier-sandbox/proto/verifier/v1"
)

type Options struct {
	Judge        *judge.Service
	Health       func(context.Context) health.Report
	Log          *slog.Logger
	MaxBodyBytes int64
	CORSOrigins  []string
	// when set, requests whose Host header is not in this list are refused, which stops
	// DNS rebinding attacks from web pages while the server only listens on loopback
	AllowedHosts []string
	// the browser interface, served at / when set
	UI fs.FS
}

type server struct {
	Options
}

var (
	marshal   = protojson.MarshalOptions{UseProtoNames: true, EmitUnpopulated: true}
	unmarshal = protojson.UnmarshalOptions{}
)

func New(opts Options) http.Handler {
	s := &server{opts}
	routes := map[string]struct {
		method  string
		handler http.HandlerFunc
	}{
		"/v1/verify":    {http.MethodPost, s.verify},
		"/v1/languages": {http.MethodGet, s.languages},
		"/healthz":      {http.MethodGet, s.healthz},
		"/readyz":       {http.MethodGet, s.readyz},
	}
	mux := http.NewServeMux()
	for path, route := range routes {
		mux.HandleFunc(route.method+" "+path, route.handler)
	}
	if s.UI != nil {
		mux.Handle("GET /{$}", pageHeaders(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.ServeFileFS(w, r, s.UI, "index.html")
		})))
		mux.Handle("GET /ui/", pageHeaders(http.StripPrefix("/ui/", http.FileServerFS(s.UI))))
	}
	// the catch all would otherwise turn a wrong method into a 404
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if route, ok := routes[r.URL.Path]; ok {
			w.Header().Set("Allow", route.method)
			writeError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", r.URL.Path+" only accepts "+route.method)
			return
		}
		writeError(w, http.StatusNotFound, "NOT_FOUND", "no route for "+r.Method+" "+r.URL.Path)
	})
	return s.recoverer(s.guard(s.cors(mux)))
}

// guard keeps random web pages in the browser from using the API. Browsers always send
// an Origin header on cross site requests, other clients like curl or verifierctl don't.
func (s *server) guard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if len(s.AllowedHosts) > 0 {
			host := r.Host
			if h, _, err := net.SplitHostPort(host); err == nil {
				host = h
			}
			host = strings.Trim(strings.ToLower(host), "[]")
			if !slices.Contains(s.AllowedHosts, host) {
				writeError(w, http.StatusForbidden, "PERMISSION_DENIED", "host "+r.Host+" is not allowed")
				return
			}
		}
		if origin := r.Header.Get("Origin"); origin != "" && !sameOrigin(r, origin) && !s.originAllowed(origin) {
			writeError(w, http.StatusForbidden, "PERMISSION_DENIED", "origin "+origin+" is not allowed, add it with -cors-origins")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *server) originAllowed(origin string) bool {
	return slices.Contains(s.CORSOrigins, "*") || slices.Contains(s.CORSOrigins, origin)
}

// our own page sends its own address as Origin. The Host check above already
// made sure that address is a loopback one, so this can't be a rebound domain.
func sameOrigin(r *http.Request, origin string) bool {
	return origin == "http://"+r.Host || origin == "https://"+r.Host
}

// pageHeaders locks the page down so it can only load its own files and can't be framed
func pageHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", "default-src 'none'; script-src 'self'; style-src 'self'; img-src 'self' data:; connect-src 'self'; base-uri 'none'; form-action 'none'; frame-ancestors 'none'")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Cache-Control", "no-cache")
		next.ServeHTTP(w, r)
	})
}

func (s *server) verify(w http.ResponseWriter, r *http.Request) {
	// a text/plain post skips the browser's CORS preflight, so only JSON is accepted
	if mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type")); err != nil || mediaType != "application/json" {
		writeError(w, http.StatusUnsupportedMediaType, "INVALID_ARGUMENT", "send the body as JSON with the header Content-Type: application/json")
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, s.MaxBodyBytes))
	if err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			writeError(w, http.StatusRequestEntityTooLarge, "RESOURCE_EXHAUSTED", "request body is larger than "+strconv.FormatInt(tooBig.Limit, 10)+" bytes")
			return
		}
		writeError(w, http.StatusBadRequest, "INVALID_ARGUMENT", "could not read request body")
		return
	}
	req := &pb.VerifyRequest{}
	if err := unmarshal.Unmarshal(body, req); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid JSON body, "+err.Error())
		return
	}

	if stream, _ := strconv.ParseBool(r.URL.Query().Get("stream")); stream {
		s.verifyStream(w, r, req)
		return
	}
	resp, err := s.Judge.Verify(r.Context(), req, nil)
	if err != nil {
		writeStatusError(w, err)
		return
	}
	writeProto(w, http.StatusOK, resp)
}

// verifyStream sends one JSON object per line as results come in. Errors found before
// the first line still get a proper status code, later ones arrive as an error line.
func (s *server) verifyStream(w http.ResponseWriter, r *http.Request, req *pb.VerifyRequest) {
	flusher, _ := w.(http.Flusher)
	started := false
	emit := func(ev *pb.VerifyEvent) error {
		if !started {
			started = true
			w.Header().Set("Content-Type", "application/x-ndjson")
			w.Header().Set("Cache-Control", "no-store")
			w.WriteHeader(http.StatusOK)
		}
		line, err := marshal.Marshal(ev)
		if err != nil {
			return err
		}
		if _, err := w.Write(append(line, '\n')); err != nil {
			return status.Error(codes.Canceled, "client went away")
		}
		if flusher != nil {
			flusher.Flush()
		}
		return nil
	}
	_, err := s.Judge.Verify(r.Context(), req, emit)
	if err == nil {
		return
	}
	if !started {
		writeStatusError(w, err)
		return
	}
	st := status.Convert(err)
	line, _ := json.Marshal(errorBody(codeName(st.Code()), st.Message()))
	w.Write(append(line, '\n'))
}

func (s *server) languages(w http.ResponseWriter, r *http.Request) {
	writeProto(w, http.StatusOK, &pb.ListLanguagesResponse{Languages: s.Judge.Languages(r.Context())})
}

func (s *server) healthz(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *server) readyz(w http.ResponseWriter, r *http.Request) {
	report := s.Health(r.Context())
	code := http.StatusOK
	if !report.Ready {
		code = http.StatusServiceUnavailable
	}
	writeJSON(w, code, report)
}

func (s *server) cors(next http.Handler) http.Handler {
	if len(s.CORSOrigins) == 0 {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		if origin != "" && s.originAllowed(origin) {
			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Add("Vary", "Origin")
			if r.Method == http.MethodOptions {
				w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
				w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
				w.Header().Set("Access-Control-Max-Age", "600")
				w.WriteHeader(http.StatusNoContent)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

func (s *server) recoverer(next http.Handler) http.Handler {
	return http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		started := time.Now()
		w := &trackingWriter{ResponseWriter: rw}
		defer func() {
			if p := recover(); p != nil {
				if p == http.ErrAbortHandler {
					panic(p)
				}
				s.Log.Error("panic in http handler", "path", r.URL.Path, "panic", p, "stack", string(debug.Stack()))
				if !w.wroteHeader {
					writeError(w, http.StatusInternalServerError, "INTERNAL", "internal server error")
				} else {
					// headers already went out mid stream, so all we can do is drop the connection
					panic(http.ErrAbortHandler)
				}
			}
			s.Log.Debug("http request", "method", r.Method, "path", r.URL.Path, "status", w.status, "took_ms", time.Since(started).Milliseconds())
		}()
		next.ServeHTTP(w, r)
	})
}

// trackingWriter remembers whether headers went out and keeps Flush working for streams.
type trackingWriter struct {
	http.ResponseWriter
	wroteHeader bool
	status      int
}

func (t *trackingWriter) WriteHeader(code int) {
	if !t.wroteHeader {
		t.wroteHeader = true
		t.status = code
	}
	t.ResponseWriter.WriteHeader(code)
}

func (t *trackingWriter) Write(b []byte) (int, error) {
	if !t.wroteHeader {
		t.WriteHeader(http.StatusOK)
	}
	return t.ResponseWriter.Write(b)
}

func (t *trackingWriter) Flush() {
	if f, ok := t.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (t *trackingWriter) Unwrap() http.ResponseWriter { return t.ResponseWriter }

func writeProto(w http.ResponseWriter, code int, m proto.Message) {
	body, err := marshal.Marshal(m)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "INTERNAL", "could not encode response")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	w.Write(append(body, '\n'))
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(v)
}

func errorBody(code, message string) map[string]any {
	return map[string]any{"error": map[string]string{"code": code, "message": message}}
}

func writeError(w http.ResponseWriter, httpCode int, code, message string) {
	writeJSON(w, httpCode, errorBody(code, message))
}

func writeStatusError(w http.ResponseWriter, err error) {
	st := status.Convert(err)
	writeError(w, httpStatus(st.Code()), codeName(st.Code()), st.Message())
}

// codeName gives the usual upper case name, like INVALID_ARGUMENT
func codeName(c codes.Code) string {
	if name, ok := rpccode.Code_name[int32(c)]; ok {
		return name
	}
	return "UNKNOWN"
}

func httpStatus(c codes.Code) int {
	switch c {
	case codes.InvalidArgument, codes.FailedPrecondition, codes.OutOfRange:
		return http.StatusBadRequest
	case codes.NotFound:
		return http.StatusNotFound
	case codes.ResourceExhausted:
		return http.StatusTooManyRequests
	case codes.Unavailable:
		return http.StatusServiceUnavailable
	case codes.DeadlineExceeded:
		return http.StatusGatewayTimeout
	case codes.Canceled:
		// nginx style "client closed request", nobody is listening anyway
		return 499
	default:
		return http.StatusInternalServerError
	}
}
