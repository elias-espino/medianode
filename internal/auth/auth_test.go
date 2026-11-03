package auth

import (
	"context"
	"errors"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/rs/zerolog"

	"medianode/internal/authorization"
	"medianode/internal/core"
	"medianode/internal/database"
	"medianode/internal/database/sqlc"
	"medianode/internal/media"
	"medianode/internal/testutil"
)

const (
	testUser     = "propietario"
	testPassword = "contraseña-segura"
)

// Costes mínimos: los valores reales harían lentas todas las pruebas.
var cheapParams = Params{MemoryKiB: 8, Iterations: 1, Threads: 1}

var testDevice = DeviceInfo{Name: "Portátil", Platform: "android", Identifier: "dev-1"}

type fixture struct {
	t     *testing.T
	ctx   context.Context
	s     *Service
	db    *database.DB
	code  string
	clock *clock
}

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *clock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

func newFixture(t *testing.T, params Params) *fixture {
	t.Helper()
	db := testutil.NewDB(t)
	c := &clock{t: time.Now()}
	s := New(db, NewHasher(params), zerolog.Nop())
	s.now = c.now
	code, err := s.Bootstrap(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return &fixture{t: t, ctx: context.Background(), s: s, db: db, code: code, clock: c}
}

func (f *fixture) owner() *sqlc.User {
	f.t.Helper()
	u, err := f.s.Setup(f.ctx, f.code, testUser, testPassword)
	if err != nil {
		f.t.Fatal(err)
	}
	return u
}

func (f *fixture) login() *TokenPair {
	f.t.Helper()
	pair, err := f.s.Login(f.ctx, testUser, testPassword, testDevice)
	if err != nil {
		f.t.Fatal(err)
	}
	return pair
}

func (f *fixture) setActive(id string, active int64) {
	f.t.Helper()
	u, err := f.db.R.GetUser(f.ctx, id)
	if err != nil {
		f.t.Fatal(err)
	}
	err = f.db.Q.UpdateUser(f.ctx, sqlc.UpdateUserParams{
		ID: id, Username: u.Username, Role: u.Role, IsActive: active, UpdatedAt: database.Now(),
	})
	if err != nil {
		f.t.Fatal(err)
	}
}

func wantCode(t *testing.T, err error, code string) {
	t.Helper()
	if got := core.CodeOf(err); got != code {
		t.Fatalf("código = %q (%v), se esperaba %q", got, err, code)
	}
}

func TestHashYVerificacion(t *testing.T) {
	h := NewHasher(DefaultParams)
	ctx := context.Background()
	encoded, err := h.Hash(ctx, testPassword)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(encoded, "$argon2id$v=19$m=65536,t=3,p=2$") {
		t.Fatalf("formato = %q", encoded)
	}
	if strings.Contains(encoded, testPassword) {
		t.Fatal("el hash contiene la contraseña")
	}
	if ok, err := h.Verify(ctx, encoded, testPassword); err != nil || !ok {
		t.Fatalf("verificar = %v %v", ok, err)
	}
	if ok, _ := h.Verify(ctx, encoded, "otra"); ok {
		t.Fatal("una contraseña errónea no debe verificarse")
	}
	other, _ := h.Hash(ctx, testPassword)
	if other == encoded {
		t.Fatal("dos hashes deben usar sales distintas")
	}
}

func TestVerificaConLosCostesGuardados(t *testing.T) {
	ctx := context.Background()
	encoded, err := NewHasher(cheapParams).Hash(ctx, testPassword)
	if err != nil {
		t.Fatal(err)
	}
	if ok, err := NewHasher(DefaultParams).Verify(ctx, encoded, testPassword); err != nil || !ok {
		t.Fatalf("verificar = %v %v", ok, err)
	}
}

func TestHashConFormatoDesconocido(t *testing.T) {
	h := NewHasher(cheapParams)
	for _, bad := range []string{"", "texto", "$argon2i$v=19$m=8,t=1,p=1$AA$AA", "$argon2id$v=1$m=8,t=1,p=1$AA$AA", "$argon2id$v=19$x$AA$AA"} {
		if _, err := h.Verify(context.Background(), bad, "x"); err == nil {
			t.Fatalf("%q debe rechazarse", bad)
		}
	}
}

func TestHasherRespetaElContexto(t *testing.T) {
	h := NewHasher(cheapParams)
	for range cap(h.slots) {
		h.slots <- struct{}{}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := h.Hash(ctx, "x"); !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v", err)
	}
}

func TestSetupFallaSinCodigoYTrasCrearOwner(t *testing.T) {
	f := newFixture(t, cheapParams)
	if f.code == "" {
		t.Fatal("el primer arranque debe generar un código")
	}
	if required, _ := f.s.SetupRequired(f.ctx); !required {
		t.Fatal("debe pedir configuración")
	}

	for _, bad := range []string{"", "ERRONEO"} {
		_, err := f.s.Setup(f.ctx, bad, testUser, testPassword)
		wantCode(t, err, core.CodeInvalidCredentials)
	}

	u := f.owner()
	if u.Role != string(media.RoleOwner) {
		t.Fatalf("rol = %q", u.Role)
	}
	if required, _ := f.s.SetupRequired(f.ctx); required {
		t.Fatal("ya no debe pedir configuración")
	}

	_, err := f.s.Setup(f.ctx, f.code, "otro-owner", testPassword)
	wantCode(t, err, core.CodeSetupCompleted)

	if _, err := f.db.R.GetSetting(f.ctx, settingSetupHash); err == nil {
		t.Fatal("el código inicial debe borrarse")
	}
}

func TestSetupAceptaCodigoConGuionesYMinusculas(t *testing.T) {
	f := newFixture(t, cheapParams)
	written := strings.ToLower(f.code[:4]) + "-" + f.code[4:]
	if _, err := f.s.Setup(f.ctx, written, testUser, testPassword); err != nil {
		t.Fatal(err)
	}
}

func TestSetupValidaCredenciales(t *testing.T) {
	f := newFixture(t, cheapParams)
	_, err := f.s.Setup(f.ctx, f.code, "ab", testPassword)
	wantCode(t, err, core.CodeValidation)
	_, err = f.s.Setup(f.ctx, f.code, testUser, "corta")
	wantCode(t, err, core.CodeValidation)
	_, err = f.s.Setup(f.ctx, f.code, "con\ncontrol", testPassword)
	wantCode(t, err, core.CodeValidation)
}

func TestSetupConcurrenteCreaUnSoloOwner(t *testing.T) {
	f := newFixture(t, cheapParams)
	var wg sync.WaitGroup
	results := make([]error, 8)
	for i := range results {
		wg.Go(func() {
			_, results[i] = f.s.Setup(f.ctx, f.code, "owner"+string(rune('a'+i)), testPassword)
		})
	}
	wg.Wait()

	ok := 0
	for _, err := range results {
		if err == nil {
			ok++
		} else if core.CodeOf(err) != core.CodeSetupCompleted {
			t.Fatalf("error = %v", err)
		}
	}
	if owners, _ := f.db.R.CountOwners(f.ctx); ok != 1 || owners != 1 {
		t.Fatalf("correctos = %d, owners = %d", ok, owners)
	}
}

func TestBootstrapSinOwnerRenuevaElCodigo(t *testing.T) {
	f := newFixture(t, cheapParams)
	second, err := f.s.Bootstrap(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	if second == "" || second == f.code {
		t.Fatalf("código = %q", second)
	}
	_, err = f.s.Setup(f.ctx, f.code, testUser, testPassword)
	wantCode(t, err, core.CodeInvalidCredentials)
	if _, err := f.s.Setup(f.ctx, second, testUser, testPassword); err != nil {
		t.Fatal(err)
	}

	third, err := f.s.Bootstrap(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	if third != "" {
		t.Fatal("con Owner no debe generarse código")
	}
}

func TestSecretoSobreviveAlReinicio(t *testing.T) {
	f := newFixture(t, cheapParams)
	f.owner()
	pair := f.login()

	restarted := New(f.db, NewHasher(cheapParams), zerolog.Nop())
	if _, err := restarted.Bootstrap(f.ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := restarted.Authenticate(f.ctx, pair.AccessToken); err != nil {
		t.Fatal(err)
	}
}

func TestBootstrapBorraTokensYCodigosCaducados(t *testing.T) {
	f := newFixture(t, cheapParams)
	f.owner()
	pair := f.login()
	if _, err := f.s.CreatePairingCode(f.ctx, DeviceInfo{Name: "TV", Platform: "tv"}); err != nil {
		t.Fatal(err)
	}
	f.clock.advance(RefreshTTL + time.Hour)
	if _, err := f.s.Bootstrap(f.ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.R.GetRefreshTokenByHash(f.ctx, hashToken(pair.RefreshToken)); err == nil {
		t.Fatal("el token caducado debe borrarse")
	}
	var n int
	if err := f.db.Read.QueryRow("SELECT COUNT(*) FROM pairing_codes").Scan(&n); err != nil || n != 0 {
		t.Fatalf("códigos = %d %v", n, err)
	}
}

func TestLoginRespondeIgualEnCadaFallo(t *testing.T) {
	f := newFixture(t, cheapParams)
	u := f.owner()

	_, err := f.s.Login(f.ctx, testUser, "errónea", testDevice)
	wantCode(t, err, core.CodeInvalidCredentials)
	_, err = f.s.Login(f.ctx, "inexistente", testPassword, testDevice)
	wantCode(t, err, core.CodeInvalidCredentials)

	f.setActive(u.ID, 0)
	_, err = f.s.Login(f.ctx, testUser, testPassword, testDevice)
	wantCode(t, err, core.CodeInvalidCredentials)
}

func TestLoginIgnoraMayusculasDelUsuario(t *testing.T) {
	f := newFixture(t, cheapParams)
	f.owner()
	if _, err := f.s.Login(f.ctx, strings.ToUpper(testUser), testPassword, testDevice); err != nil {
		t.Fatal(err)
	}
}

func TestLoginRegistraUltimoAcceso(t *testing.T) {
	f := newFixture(t, cheapParams)
	u := f.owner()
	f.login()
	got, err := f.db.R.GetUser(f.ctx, u.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.LastLoginAt == nil {
		t.Fatal("falta last_login_at")
	}
}

func TestLoginExigeDatosDelDispositivo(t *testing.T) {
	f := newFixture(t, cheapParams)
	f.owner()
	_, err := f.s.Login(f.ctx, testUser, testPassword, DeviceInfo{Platform: "android"})
	wantCode(t, err, core.CodeValidation)
	_, err = f.s.Login(f.ctx, testUser, testPassword, DeviceInfo{Name: "x"})
	wantCode(t, err, core.CodeValidation)
	_, err = f.s.Login(f.ctx, testUser, testPassword, DeviceInfo{Name: strings.Repeat("x", 101), Platform: "tv"})
	wantCode(t, err, core.CodeValidation)
}

func TestLimiteDeIntentosPorCuenta(t *testing.T) {
	f := newFixture(t, cheapParams)
	f.owner()
	for range accountAttemptsPerMinute {
		_, err := f.s.Login(f.ctx, testUser, "errónea", testDevice)
		wantCode(t, err, core.CodeInvalidCredentials)
	}
	// La contraseña correcta tampoco pasa, y variar mayúsculas no esquiva el límite.
	_, err := f.s.Login(f.ctx, strings.ToUpper(testUser), testPassword, testDevice)
	wantCode(t, err, core.CodeRateLimited)
}

// La diferencia que se busca es de un orden de magnitud: sin el hash falso, el
// usuario inexistente responde sin calcular nada.
func TestLoginSinUsuarioTardaLoMismoQueConContrasenaErronea(t *testing.T) {
	if testing.Short() {
		t.Skip("usa los costes reales de Argon2id")
	}
	f := newFixture(t, DefaultParams)
	f.owner()

	median := func(username string) time.Duration {
		samples := make([]time.Duration, 5)
		for i := range samples {
			f.s.accounts = NewLimiter(accountAttemptsPerMinute)
			start := time.Now()
			_, err := f.s.Login(f.ctx, username, "errónea", testDevice)
			samples[i] = time.Since(start)
			wantCode(t, err, core.CodeInvalidCredentials)
		}
		sort.Slice(samples, func(i, j int) bool { return samples[i] < samples[j] })
		return samples[len(samples)/2]
	}

	existing, missing := median(testUser), median("inexistente")
	if missing < existing/2 || missing > existing*2 {
		t.Fatalf("usuario existente %v, inexistente %v", existing, missing)
	}
}

func TestDispositivoSeReutilizaPorIdentificador(t *testing.T) {
	f := newFixture(t, cheapParams)
	f.owner()
	first := f.login()
	second := f.login()
	if first.DeviceID != second.DeviceID {
		t.Fatal("el mismo cliente debe reutilizar su dispositivo")
	}
	// Solo queda vigente la cadena del último login.
	_, err := f.s.Refresh(f.ctx, first.RefreshToken)
	wantCode(t, err, core.CodeInvalidCredentials)
	if _, err := f.s.Refresh(f.ctx, second.RefreshToken); err != nil {
		t.Fatal(err)
	}

	noID := DeviceInfo{Name: "x", Platform: "web"}
	a, err := f.s.Login(f.ctx, testUser, testPassword, noID)
	if err != nil {
		t.Fatal(err)
	}
	b, err := f.s.Login(f.ctx, testUser, testPassword, noID)
	if err != nil {
		t.Fatal(err)
	}
	if a.DeviceID == b.DeviceID {
		t.Fatal("sin identificador cada login es un dispositivo")
	}
}

func TestDispositivoRevocadoNoSeReactiva(t *testing.T) {
	f := newFixture(t, cheapParams)
	f.owner()
	first := f.login()
	if err := f.s.Logout(f.ctx, first.DeviceID); err != nil {
		t.Fatal(err)
	}
	second := f.login()
	if second.DeviceID == first.DeviceID {
		t.Fatal("debe crear una fila nueva")
	}
	_, err := f.s.Authenticate(f.ctx, first.AccessToken)
	wantCode(t, err, core.CodeDeviceRevoked)
}

func TestAuthenticate(t *testing.T) {
	f := newFixture(t, cheapParams)
	u := f.owner()
	pair := f.login()

	actor, err := f.s.Authenticate(f.ctx, pair.AccessToken)
	if err != nil {
		t.Fatal(err)
	}
	want := authorization.Actor{UserID: u.ID, DeviceID: pair.DeviceID, Role: media.RoleOwner}
	if actor != want {
		t.Fatalf("actor = %+v", actor)
	}

	_, err = f.s.Authenticate(f.ctx, "no-es-un-token")
	wantCode(t, err, core.CodeAuthRequired)
	_, err = f.s.Authenticate(f.ctx, pair.AccessToken+"x")
	wantCode(t, err, core.CodeAuthRequired)

	f.clock.advance(AccessTTL + time.Second)
	_, err = f.s.Authenticate(f.ctx, pair.AccessToken)
	wantCode(t, err, core.CodeAuthTokenExpired)
}

func TestAuthenticateRechazaTokensForjados(t *testing.T) {
	f := newFixture(t, cheapParams)
	u := f.owner()
	pair := f.login()
	c := claims{DeviceID: pair.DeviceID, RegisteredClaims: jwt.RegisteredClaims{
		Subject:   u.ID,
		ExpiresAt: jwt.NewNumericDate(f.clock.now().Add(time.Hour)),
	}}

	none, err := jwt.NewWithClaims(jwt.SigningMethodNone, c).SignedString(jwt.UnsafeAllowNoneSignatureType)
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.s.Authenticate(f.ctx, none)
	wantCode(t, err, core.CodeAuthRequired)

	other, err := jwt.NewWithClaims(jwt.SigningMethodHS256, c).SignedString([]byte("otro secreto de treinta y dos bytes"))
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.s.Authenticate(f.ctx, other)
	wantCode(t, err, core.CodeAuthRequired)

	hs512, err := jwt.NewWithClaims(jwt.SigningMethodHS512, c).SignedString(f.s.secret)
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.s.Authenticate(f.ctx, hs512)
	wantCode(t, err, core.CodeAuthRequired)

	noExpiry, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims{
		DeviceID: pair.DeviceID, RegisteredClaims: jwt.RegisteredClaims{Subject: u.ID},
	}).SignedString(f.s.secret)
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.s.Authenticate(f.ctx, noExpiry)
	wantCode(t, err, core.CodeAuthRequired)
}

// El access token sigue vigente en todos estos casos: el corte viene de la base.
func TestCambiosDeCuentaSurtenEfectoEnLaSiguientePeticion(t *testing.T) {
	f := newFixture(t, cheapParams)
	u := f.owner()
	pair := f.login()

	t.Run("cambio de rol", func(t *testing.T) {
		if _, err := f.db.Write.Exec("DROP INDEX idx_users_one_owner"); err != nil {
			t.Fatal(err)
		}
		err := f.db.Q.UpdateUser(f.ctx, sqlc.UpdateUserParams{
			ID: u.ID, Username: u.Username, Role: string(media.RoleRestricted), IsActive: 1, UpdatedAt: database.Now(),
		})
		if err != nil {
			t.Fatal(err)
		}
		actor, err := f.s.Authenticate(f.ctx, pair.AccessToken)
		if err != nil {
			t.Fatal(err)
		}
		if actor.Role != media.RoleRestricted {
			t.Fatalf("rol = %q", actor.Role)
		}
	})

	t.Run("cuenta desactivada", func(t *testing.T) {
		f.setActive(u.ID, 0)
		_, err := f.s.Authenticate(f.ctx, pair.AccessToken)
		wantCode(t, err, core.CodeAuthRequired)
		_, err = f.s.Refresh(f.ctx, pair.RefreshToken)
		wantCode(t, err, core.CodeInvalidCredentials)
		f.setActive(u.ID, 1)
	})

	t.Run("cuenta eliminada", func(t *testing.T) {
		if err := f.db.Q.DeleteUser(f.ctx, u.ID); err != nil {
			t.Fatal(err)
		}
		_, err := f.s.Authenticate(f.ctx, pair.AccessToken)
		wantCode(t, err, core.CodeAuthRequired)
	})
}

func TestLogoutCortaElAccesoDeInmediato(t *testing.T) {
	f := newFixture(t, cheapParams)
	f.owner()
	pair := f.login()
	if err := f.s.Logout(f.ctx, pair.DeviceID); err != nil {
		t.Fatal(err)
	}
	_, err := f.s.Authenticate(f.ctx, pair.AccessToken)
	wantCode(t, err, core.CodeDeviceRevoked)
	_, err = f.s.Refresh(f.ctx, pair.RefreshToken)
	wantCode(t, err, core.CodeInvalidCredentials)
}

func TestRefreshRota(t *testing.T) {
	f := newFixture(t, cheapParams)
	f.owner()
	first := f.login()
	second, err := f.s.Refresh(f.ctx, first.RefreshToken)
	if err != nil {
		t.Fatal(err)
	}
	if second.RefreshToken == first.RefreshToken || second.DeviceID != first.DeviceID {
		t.Fatalf("par = %+v", second)
	}
	if _, err := f.s.Authenticate(f.ctx, second.AccessToken); err != nil {
		t.Fatal(err)
	}

	stored, err := f.db.R.GetRefreshTokenByHash(f.ctx, hashToken(first.RefreshToken))
	if err != nil {
		t.Fatal(err)
	}
	if stored.RotatedAt == nil {
		t.Fatal("falta rotated_at")
	}
	if strings.Contains(stored.TokenHash, first.RefreshToken) {
		t.Fatal("el token no debe guardarse en claro")
	}
}

func TestRefreshInvalido(t *testing.T) {
	f := newFixture(t, cheapParams)
	f.owner()
	pair := f.login()

	_, err := f.s.Refresh(f.ctx, "desconocido")
	wantCode(t, err, core.CodeInvalidCredentials)

	f.clock.advance(RefreshTTL + time.Second)
	_, err = f.s.Refresh(f.ctx, pair.RefreshToken)
	wantCode(t, err, core.CodeInvalidCredentials)
}

func TestReutilizarTokenRotadoRevocaElDispositivo(t *testing.T) {
	f := newFixture(t, cheapParams)
	f.owner()
	first := f.login()
	second, err := f.s.Refresh(f.ctx, first.RefreshToken)
	if err != nil {
		t.Fatal(err)
	}

	f.clock.advance(retryWindow + time.Second)
	_, err = f.s.Refresh(f.ctx, first.RefreshToken)
	wantCode(t, err, core.CodeDeviceRevoked)

	// La revocación debe confirmarse aunque la llamada termine en error.
	dev, err := f.db.R.GetDevice(f.ctx, first.DeviceID)
	if err != nil {
		t.Fatal(err)
	}
	if dev.RevokedAt == nil {
		t.Fatal("el dispositivo debe quedar revocado")
	}
	_, err = f.s.Authenticate(f.ctx, second.AccessToken)
	wantCode(t, err, core.CodeDeviceRevoked)
	_, err = f.s.Refresh(f.ctx, second.RefreshToken)
	wantCode(t, err, core.CodeInvalidCredentials)
}

func TestReintentoDentroDeLaVentanaSustituyeAlSucesor(t *testing.T) {
	f := newFixture(t, cheapParams)
	f.owner()
	first := f.login()
	lost, err := f.s.Refresh(f.ctx, first.RefreshToken)
	if err != nil {
		t.Fatal(err)
	}

	f.clock.advance(retryWindow - time.Second)
	retried, err := f.s.Refresh(f.ctx, first.RefreshToken)
	if err != nil {
		t.Fatal(err)
	}

	_, err = f.s.Refresh(f.ctx, lost.RefreshToken)
	wantCode(t, err, core.CodeInvalidCredentials)
	if _, err := f.s.Refresh(f.ctx, retried.RefreshToken); err != nil {
		t.Fatal(err)
	}
}

// Un reintento no amplía la ventana, que cuenta desde la primera rotación.
func TestLaVentanaNoSeRenuevaConCadaReintento(t *testing.T) {
	f := newFixture(t, cheapParams)
	f.owner()
	first := f.login()
	if _, err := f.s.Refresh(f.ctx, first.RefreshToken); err != nil {
		t.Fatal(err)
	}
	f.clock.advance(retryWindow - time.Second)
	if _, err := f.s.Refresh(f.ctx, first.RefreshToken); err != nil {
		t.Fatal(err)
	}
	f.clock.advance(2 * time.Second)
	_, err := f.s.Refresh(f.ctx, first.RefreshToken)
	wantCode(t, err, core.CodeDeviceRevoked)
}

func TestRefreshConcurrenteDejaUnSoloTokenVigente(t *testing.T) {
	f := newFixture(t, cheapParams)
	f.owner()
	first := f.login()

	var wg sync.WaitGroup
	pairs := make([]*TokenPair, 8)
	errs := make([]error, len(pairs))
	for i := range pairs {
		wg.Go(func() { pairs[i], errs[i] = f.s.Refresh(f.ctx, first.RefreshToken) })
	}
	wg.Wait()

	valid := 0
	for i := range pairs {
		if errs[i] != nil {
			t.Fatal(errs[i])
		}
		row, err := f.db.R.GetRefreshTokenByHash(f.ctx, hashToken(pairs[i].RefreshToken))
		if err != nil {
			t.Fatal(err)
		}
		if row.RevokedAt == nil && row.RotatedAt == nil {
			valid++
		}
	}
	if valid != 1 {
		t.Fatalf("tokens vigentes = %d", valid)
	}
}

func TestRefreshBorraLosCaducadosDelDispositivo(t *testing.T) {
	f := newFixture(t, cheapParams)
	f.owner()
	first := f.login()
	f.clock.advance(RefreshTTL / 2)
	second, err := f.s.Refresh(f.ctx, first.RefreshToken)
	if err != nil {
		t.Fatal(err)
	}
	f.clock.advance(RefreshTTL/2 + time.Second)
	if _, err := f.s.Refresh(f.ctx, second.RefreshToken); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.R.GetRefreshTokenByHash(f.ctx, hashToken(first.RefreshToken)); err == nil {
		t.Fatal("el token caducado debe borrarse")
	}
}

func TestVinculacionCompleta(t *testing.T) {
	f := newFixture(t, cheapParams)
	u := f.owner()
	session := f.login()
	actor, err := f.s.Authenticate(f.ctx, session.AccessToken)
	if err != nil {
		t.Fatal(err)
	}

	tv := DeviceInfo{Name: "TV del salón", Platform: "androidtv", Identifier: "tv-1"}
	req, err := f.s.CreatePairingCode(f.ctx, tv)
	if err != nil {
		t.Fatal(err)
	}
	if len(req.Code) != pairingLength {
		t.Fatalf("código = %q", req.Code)
	}

	pending, err := f.s.PollPairing(f.ctx, req.PollToken)
	if err != nil || pending != nil {
		t.Fatalf("antes de reclamar: %v %v", pending, err)
	}

	claim, err := f.s.ClaimPairingCode(f.ctx, actor, strings.ToLower(req.Code))
	if err != nil {
		t.Fatal(err)
	}
	if claim.DeviceName != tv.Name {
		t.Fatalf("reclamado = %+v", claim)
	}

	pair, err := f.s.PollPairing(f.ctx, req.PollToken)
	if err != nil {
		t.Fatal(err)
	}
	if pair == nil || pair.User.ID != u.ID || pair.DeviceID == session.DeviceID {
		t.Fatalf("par = %+v", pair)
	}
	tvActor, err := f.s.Authenticate(f.ctx, pair.AccessToken)
	if err != nil {
		t.Fatal(err)
	}
	if tvActor.UserID != u.ID {
		t.Fatalf("actor = %+v", tvActor)
	}
	dev, err := f.db.R.GetDevice(f.ctx, pair.DeviceID)
	if err != nil {
		t.Fatal(err)
	}
	if dev.DeviceName != tv.Name || dev.Platform != tv.Platform {
		t.Fatalf("dispositivo = %+v", dev)
	}

	// Un solo uso, en los dos sentidos.
	_, err = f.s.PollPairing(f.ctx, req.PollToken)
	wantCode(t, err, core.CodePairingInvalid)
	_, err = f.s.ClaimPairingCode(f.ctx, actor, req.Code)
	wantCode(t, err, core.CodePairingInvalid)
}

func TestVinculacionCaducada(t *testing.T) {
	f := newFixture(t, cheapParams)
	f.owner()
	actor, _ := f.s.Authenticate(f.ctx, f.login().AccessToken)

	req, err := f.s.CreatePairingCode(f.ctx, DeviceInfo{Name: "TV", Platform: "tv"})
	if err != nil {
		t.Fatal(err)
	}
	f.clock.advance(PairingTTL + time.Second)
	_, err = f.s.ClaimPairingCode(f.ctx, actor, req.Code)
	wantCode(t, err, core.CodePairingInvalid)
	_, err = f.s.PollPairing(f.ctx, req.PollToken)
	wantCode(t, err, core.CodePairingInvalid)
}

func TestVinculacionConDatosErroneos(t *testing.T) {
	f := newFixture(t, cheapParams)
	f.owner()
	actor, _ := f.s.Authenticate(f.ctx, f.login().AccessToken)

	_, err := f.s.ClaimPairingCode(f.ctx, actor, "XXXXXX")
	wantCode(t, err, core.CodePairingInvalid)
	_, err = f.s.PollPairing(f.ctx, "desconocido")
	wantCode(t, err, core.CodePairingInvalid)
	_, err = f.s.CreatePairingCode(f.ctx, DeviceInfo{})
	wantCode(t, err, core.CodeValidation)
}

func TestVinculacionRechazaAlUsuarioDesactivado(t *testing.T) {
	f := newFixture(t, cheapParams)
	u := f.owner()
	actor, _ := f.s.Authenticate(f.ctx, f.login().AccessToken)
	req, err := f.s.CreatePairingCode(f.ctx, DeviceInfo{Name: "TV", Platform: "tv"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.ClaimPairingCode(f.ctx, actor, req.Code); err != nil {
		t.Fatal(err)
	}
	f.setActive(u.ID, 0)
	_, err = f.s.PollPairing(f.ctx, req.PollToken)
	wantCode(t, err, core.CodePairingInvalid)
}

func TestLimiteDeReclamaciones(t *testing.T) {
	f := newFixture(t, cheapParams)
	actor := authorization.Actor{UserID: "u1"}
	for range pairingAttemptsPerMinute {
		_, err := f.s.ClaimPairingCode(f.ctx, actor, "XXXXXX")
		wantCode(t, err, core.CodePairingInvalid)
	}
	_, err := f.s.ClaimPairingCode(f.ctx, actor, "XXXXXX")
	wantCode(t, err, core.CodeRateLimited)
}

// Repartir los intentos entre cuentas no debe sumar más que el tope global.
func TestLimiteGlobalDeReclamaciones(t *testing.T) {
	f := newFixture(t, cheapParams)
	for i := range pairingGlobalPerMinute {
		actor := authorization.Actor{UserID: "cuenta-" + string(rune('a'+i))}
		_, err := f.s.ClaimPairingCode(f.ctx, actor, "XXXXXX")
		wantCode(t, err, core.CodePairingInvalid)
	}
	_, err := f.s.ClaimPairingCode(f.ctx, authorization.Actor{UserID: "otra"}, "XXXXXX")
	wantCode(t, err, core.CodeRateLimited)
}

func TestCodigoDeVinculacionUsaElAlfabeto(t *testing.T) {
	for range 200 {
		code, err := randomPairingCode()
		if err != nil {
			t.Fatal(err)
		}
		if strings.Trim(code, pairingAlphabet) != "" {
			t.Fatalf("código = %q", code)
		}
	}
	if len(pairingAlphabet) != 32 {
		t.Fatal("el alfabeto debe tener 32 símbolos para no introducir sesgo")
	}
}

func TestCambiarContrasena(t *testing.T) {
	f := newFixture(t, cheapParams)
	f.owner()
	current := f.login()
	other, err := f.s.Login(f.ctx, testUser, testPassword, DeviceInfo{Name: "Otro", Platform: "ios", Identifier: "dev-2"})
	if err != nil {
		t.Fatal(err)
	}
	actor, err := f.s.Authenticate(f.ctx, current.AccessToken)
	if err != nil {
		t.Fatal(err)
	}

	err = f.s.ChangePassword(f.ctx, actor, "errónea", "contraseña-nueva")
	wantCode(t, err, core.CodeInvalidCredentials)
	err = f.s.ChangePassword(f.ctx, actor, testPassword, "corta")
	wantCode(t, err, core.CodeValidation)

	if err := f.s.ChangePassword(f.ctx, actor, testPassword, "contraseña-nueva"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.Authenticate(f.ctx, current.AccessToken); err != nil {
		t.Fatalf("el dispositivo actual debe seguir activo: %v", err)
	}
	_, err = f.s.Authenticate(f.ctx, other.AccessToken)
	wantCode(t, err, core.CodeDeviceRevoked)
	_, err = f.s.Refresh(f.ctx, other.RefreshToken)
	wantCode(t, err, core.CodeInvalidCredentials)

	_, err = f.s.Login(f.ctx, testUser, testPassword, testDevice)
	wantCode(t, err, core.CodeInvalidCredentials)
	if _, err := f.s.Login(f.ctx, testUser, "contraseña-nueva", testDevice); err != nil {
		t.Fatal(err)
	}
}

func TestLimiteDeCambioDeContrasena(t *testing.T) {
	f := newFixture(t, cheapParams)
	f.owner()
	actor, _ := f.s.Authenticate(f.ctx, f.login().AccessToken)
	for range accountAttemptsPerMinute {
		_ = f.s.ChangePassword(f.ctx, actor, "errónea", "contraseña-nueva")
	}
	err := f.s.ChangePassword(f.ctx, actor, testPassword, "contraseña-nueva")
	wantCode(t, err, core.CodeRateLimited)
}

func TestLimiterSeRellenaYBarreEntradas(t *testing.T) {
	t0 := time.Now()
	now := t0
	l := NewLimiter(2)
	l.now = func() time.Time { return now }

	if !l.Allow("a") || !l.Allow("a") {
		t.Fatal("debe admitir el cupo")
	}
	if l.Allow("a") {
		t.Fatal("debe rechazar al superar el cupo")
	}
	if !l.Allow("b") {
		t.Fatal("cada clave tiene su cupo")
	}
	now = now.Add(31 * time.Second)
	if !l.Allow("a") {
		t.Fatal("el cupo debe recuperarse con el tiempo")
	}

	for i := range 1000 {
		l.Allow("clave-" + string(rune(i)))
	}
	now = now.Add(3 * idleAfter)
	l.Allow("final")
	if len(l.entries) != 1 {
		t.Fatalf("entradas = %d", len(l.entries))
	}
}
