package logging

import (
	"context"
	"net/http"
	"os"

	"github.com/rs/xid"
	"github.com/sirupsen/logrus"
)

type requestKey struct{}

var logger = newLogger() //nolint:gochecknoglobals // One configured logger serves all requests.

func newLogger() *logrus.Logger {
	l := logrus.New()
	l.SetOutput(os.Stderr)
	l.SetFormatter(&logrus.JSONFormatter{})
	return l
}

func From(ctx context.Context) *logrus.Entry {
	entry := logrus.NewEntry(logger).WithContext(ctx)
	if id, ok := ctx.Value(requestKey{}).(string); ok {
		entry = entry.WithField("request_id", id)
	}
	return entry
}

func RequestID(ctx context.Context) string {
	id, _ := ctx.Value(requestKey{}).(string)
	return id
}

func Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := xid.New().String()
		ctx := context.WithValue(r.Context(), requestKey{}, id)
		w.Header().Set("X-Request-ID", id)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}
