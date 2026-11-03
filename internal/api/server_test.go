package api

import (
	"context"
	"io"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/rs/zerolog"
)

func TestServerPuertoOcupadoFallaAlArrancar(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	s := NewServer(ln.Addr().String(), http.NewServeMux(), zerolog.Nop(), func(error) {})
	if err := s.Start(context.Background()); err == nil {
		t.Fatal("se esperaba un error")
	}
}

func TestServerStopEsperaPeticionesEnCurso(t *testing.T) {
	entered := make(chan struct{})
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(entered)
		time.Sleep(100 * time.Millisecond)
		_, _ = io.WriteString(w, "fin")
	})

	s := NewServer("127.0.0.1:0", h, zerolog.Nop(), func(err error) { t.Error(err) })
	if err := s.Start(context.Background()); err != nil {
		t.Fatal(err)
	}

	body := make(chan string, 1)
	go func() {
		resp, err := http.Get("http://" + s.Addr() + "/")
		if err != nil {
			t.Error(err)
			body <- ""
			return
		}
		defer resp.Body.Close()
		data, _ := io.ReadAll(resp.Body)
		body <- string(data)
	}()

	<-entered
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := s.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	if got := <-body; got != "fin" {
		t.Fatalf("respuesta = %q", got)
	}
}

func TestServerStopCortaAlAgotarElPlazo(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(entered)
		<-release
	})

	s := NewServer("127.0.0.1:0", h, zerolog.Nop(), func(err error) { t.Error(err) })
	if err := s.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer close(release)

	go func() {
		if resp, err := http.Get("http://" + s.Addr() + "/"); err == nil {
			resp.Body.Close()
		}
	}()
	<-entered

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if err := s.Stop(ctx); err == nil {
		t.Fatal("se esperaba un error de plazo")
	}
}
