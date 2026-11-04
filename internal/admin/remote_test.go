package admin

import (
	"context"
	"net"
	"strings"
	"testing"

	"github.com/rs/zerolog"

	"medianode/internal/config"
	"medianode/internal/events"
	"medianode/internal/sessions"
	"medianode/internal/testutil"
)

func networkService(t *testing.T, mutate func(*config.Config)) *Service {
	t.Helper()
	db := testutil.NewDB(t)
	cfg := *config.Default()
	mutate(&cfg)
	return &Service{Config: cfg, DB: db, Sessions: sessions.New(db, events.NewBus(), sessions.Options{}, zerolog.Nop())}
}

func TestNetworkInformaDelAccesoRemotoYDeDLNA(t *testing.T) {
	old := interfaceAddrs
	t.Cleanup(func() { interfaceAddrs = old })
	interfaceAddrs = func() ([]net.Addr, error) {
		ip, n, _ := net.ParseCIDR("192.168.1.22/24")
		n.IP = ip
		return []net.Addr{n}, nil
	}

	// Por defecto, todo apagado y solo HTTP.
	n := networkService(t, func(*config.Config) {}).Network()
	if n.TLSEnabled || n.RemoteEnabled || n.RemoteAdmin || n.DLNAEnabled || n.PublicURL != "" || len(n.LocalNetworks) != 0 ||
		len(n.Addresses) != 1 || n.Addresses[0] != "http://192.168.1.22:8420" {
		t.Fatalf("%+v", n)
	}

	// Con HTTPS se anuncia también su dirección.
	n = networkService(t, func(c *config.Config) { c.Server.HTTPSEnabled = true }).Network()
	if !n.TLSEnabled || strings.Join(n.Addresses, ",") != "http://192.168.1.22:8420,https://192.168.1.22:8421" {
		t.Fatalf("%+v", n)
	}

	// El acceso remoto solo cuenta si están el interruptor general y el directo.
	for name, c := range map[string]struct {
		enabled, direct, admin bool
		wantRemote, wantAdmin  bool
	}{
		"solo enabled":          {true, false, false, false, false},
		"solo direct":           {false, true, false, false, false},
		"enabled y direct":      {true, true, false, true, false},
		"con administración":    {true, true, true, true, true},
		"admin sin direct":      {true, false, true, false, false},
		"admin sin interruptor": {false, true, true, false, false},
	} {
		n := networkService(t, func(cfg *config.Config) {
			cfg.Remote.Enabled, cfg.Remote.DirectEnabled, cfg.Remote.AdminEnabled = c.enabled, c.direct, c.admin
			cfg.Remote.PublicURL = "https://media.example.com"
			cfg.Remote.LocalNetworks = []string{"100.64.0.0/10"}
			cfg.Discovery.DLNAEnabled = true
		}).Network()
		if n.RemoteEnabled != c.wantRemote || n.RemoteAdmin != c.wantAdmin {
			t.Errorf("%s: remoto=%v admin=%v", name, n.RemoteEnabled, n.RemoteAdmin)
		}
		if n.PublicURL != "https://media.example.com" || strings.Join(n.LocalNetworks, ",") != "100.64.0.0/10" || !n.DLNAEnabled {
			t.Errorf("%s: %+v", name, n)
		}
	}
}

func TestComprobacionDeDLNAYDescubrimiento(t *testing.T) {
	ctx := context.Background()
	s := networkService(t, func(*config.Config) {})
	if c := s.checkDiscovery(ctx); c.Status != CheckSkipped || !strings.Contains(c.Message, "desactivado") {
		t.Fatalf("desactivado: %+v", c)
	}
	s.Config.Discovery.DLNAEnabled = true
	if c := s.checkDiscovery(ctx); c.Status != CheckWarn || !strings.Contains(c.Message, "ninguna biblioteca") {
		t.Fatalf("sin bibliotecas publicadas: %+v", c)
	}
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := s.DB.Write.Exec(q, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec("INSERT INTO libraries (id, name, type, dlna_enabled) VALUES ('a', 'Cine', 'movies', 1)")
	c := s.checkDiscovery(ctx)
	if c.Status != CheckOK || !strings.Contains(c.Message, "1 biblioteca") || !strings.Contains(c.Message, "sin contraseña") {
		t.Fatalf("una publicada: %+v", c)
	}
	exec("INSERT INTO libraries (id, name, type, dlna_enabled) VALUES ('b', 'Series', 'tv', 1)")
	exec("INSERT INTO libraries (id, name, type, dlna_enabled) VALUES ('c', 'Privada', 'movies', 0)")
	exec("INSERT INTO libraries (id, name, type, dlna_enabled, enabled) VALUES ('d', 'Apagada', 'movies', 1, 0)")
	if c := s.checkDiscovery(ctx); c.Status != CheckOK || !strings.Contains(c.Message, "2 biblioteca") {
		t.Fatalf("solo cuentan las publicadas y activas: %+v", c)
	}
}
