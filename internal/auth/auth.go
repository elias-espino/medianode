// Package auth gestiona contraseñas, tokens y vinculación de dispositivos.
package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/base32"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/golang-jwt/jwt/v5"
	"github.com/rs/zerolog"

	"medianode/internal/authorization"
	"medianode/internal/core"
	"medianode/internal/database"
	"medianode/internal/database/sqlc"
	"medianode/internal/devices"
	"medianode/internal/media"
)

const (
	// AccessTTL es la vida del access token.
	AccessTTL = 15 * time.Minute
	// RefreshTTL es la vida del refresh token.
	RefreshTTL = 90 * 24 * time.Hour
	// PairingTTL es la vida de un código de vinculación.
	PairingTTL = 5 * time.Minute

	// retryWindow admite reintentar un refresh cuya respuesta se perdió.
	retryWindow = 30 * time.Second

	secretBytes    = 32
	setupCodeBytes = 16
	pairingLength  = 6
	// Sin 0/O ni 1/I, que se confunden al copiarlos de una pantalla.
	pairingAlphabet = "23456789ABCDEFGHJKLMNPQRSTUVWXYZ"

	settingJWTSecret = "auth.jwt_secret"
	settingSetupHash = "auth.setup_code_hash"

	minUsernameLength = 3
	maxUsernameLength = 32
	minPasswordLength = 8
	maxPasswordLength = 1024
	maxDeviceField    = 100

	accountAttemptsPerMinute = 5
	pairingAttemptsPerMinute = 5
	pairingGlobalPerMinute   = 30
	globalPairingKey         = "*"
)

// DeviceInfo describe al cliente que se identifica. IP solo va a los logs.
type DeviceInfo struct {
	Name          string
	Platform      string
	ClientVersion string
	Identifier    string
	IP            string
}

// TokenPair es el resultado de un login, un refresh o una vinculación.
type TokenPair struct {
	AccessToken  string
	RefreshToken string
	ExpiresAt    time.Time
	User         sqlc.User
	DeviceID     string
}

// PairingRequest es lo que recibe el dispositivo que pide vincularse.
type PairingRequest struct {
	Code      string
	PollToken string
	ExpiresAt time.Time
}

// PairingClaim describe al dispositivo reclamado, para que el usuario confirme
// que es el suyo.
type PairingClaim struct {
	DeviceName string
	Platform   string
}

type claims struct {
	DeviceID string `json:"did"`
	jwt.RegisteredClaims
}

// Service agrupa la autenticación.
type Service struct {
	db       *database.DB
	hasher   *Hasher
	log      zerolog.Logger
	secret   []byte
	dummy    string
	accounts *Limiter
	claimers *Limiter
	now      func() time.Time
}

// New crea el servicio. Bootstrap debe ejecutarse antes de usarlo.
func New(db *database.DB, hasher *Hasher, log zerolog.Logger) *Service {
	return &Service{
		db:       db,
		hasher:   hasher,
		log:      log,
		accounts: NewLimiter(accountAttemptsPerMinute),
		claimers: NewLimiter(pairingGlobalPerMinute),
		now:      time.Now,
	}
}

// Bootstrap prepara el secreto de firma y limpia los tokens caducados. Mientras
// no exista el Owner devuelve un código inicial nuevo: solo se guarda su hash, de
// modo que tras un reinicio el anterior ya no se puede volver a mostrar.
func (s *Service) Bootstrap(ctx context.Context) (string, error) {
	// El hash falso iguala el tiempo de un login sin usuario existente.
	dummy, err := s.hasher.Hash(ctx, "medianode")
	if err != nil {
		return "", err
	}
	s.dummy = dummy

	var setupCode string
	err = s.db.InTx(ctx, func(q *sqlc.Queries) error {
		now := database.FormatTime(s.now())
		if err := q.DeleteExpiredRefreshTokens(ctx, now); err != nil {
			return fmt.Errorf("limpiar tokens: %w", err)
		}
		if err := q.DeleteExpiredPairingCodes(ctx, now); err != nil {
			return fmt.Errorf("limpiar códigos: %w", err)
		}

		secret, err := loadSecret(ctx, q)
		if err != nil {
			return err
		}
		s.secret = secret

		owners, err := q.CountOwners(ctx)
		if err != nil {
			return fmt.Errorf("contar owners: %w", err)
		}
		if owners > 0 {
			return nil
		}
		raw, err := randomBytes(setupCodeBytes)
		if err != nil {
			return err
		}
		setupCode = base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(raw)
		return q.SetSetting(ctx, sqlc.SetSettingParams{Key: settingSetupHash, Value: hashToken(setupCode)})
	})
	if err != nil {
		return "", fmt.Errorf("preparar autenticación: %w", err)
	}
	return setupCode, nil
}

func loadSecret(ctx context.Context, q *sqlc.Queries) ([]byte, error) {
	stored, err := q.GetSetting(ctx, settingJWTSecret)
	if err == nil {
		secret, err := base64.StdEncoding.DecodeString(stored)
		if err != nil {
			return nil, fmt.Errorf("secreto JWT dañado: %w", err)
		}
		return secret, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("leer secreto JWT: %w", err)
	}
	secret, err := randomBytes(secretBytes)
	if err != nil {
		return nil, err
	}
	err = q.SetSetting(ctx, sqlc.SetSettingParams{
		Key:   settingJWTSecret,
		Value: base64.StdEncoding.EncodeToString(secret),
	})
	if err != nil {
		return nil, fmt.Errorf("guardar secreto JWT: %w", err)
	}
	return secret, nil
}

// SetupRequired indica si falta crear el Owner.
func (s *Service) SetupRequired(ctx context.Context) (bool, error) {
	owners, err := s.db.R.CountOwners(ctx)
	if err != nil {
		return false, fmt.Errorf("contar owners: %w", err)
	}
	return owners == 0, nil
}

// Setup crea el Owner con el código inicial, que queda invalidado.
func (s *Service) Setup(ctx context.Context, code, username, password string) (*sqlc.User, error) {
	username, err := ValidateCredentials(username, password)
	if err != nil {
		return nil, err
	}
	// El cálculo queda fuera de la transacción: bloquearía al único escritor.
	hash, err := s.hasher.Hash(ctx, password)
	if err != nil {
		return nil, err
	}
	id, err := database.NewID()
	if err != nil {
		return nil, err
	}

	var user sqlc.User
	err = s.db.InTx(ctx, func(q *sqlc.Queries) error {
		owners, err := q.CountOwners(ctx)
		if err != nil {
			return fmt.Errorf("contar owners: %w", err)
		}
		if owners > 0 {
			return core.ErrSetupCompleted()
		}
		want, err := q.GetSetting(ctx, settingSetupHash)
		if errors.Is(err, sql.ErrNoRows) {
			return core.ErrInvalidCredentials()
		}
		if err != nil {
			return fmt.Errorf("leer código inicial: %w", err)
		}
		if !hashesEqual(hashToken(normalizeCode(code)), want) {
			return core.ErrInvalidCredentials()
		}

		err = q.CreateUser(ctx, sqlc.CreateUserParams{
			ID: id, Username: username, PasswordHash: hash, Role: string(media.RoleOwner),
		})
		if err != nil {
			return fmt.Errorf("crear owner: %w", err)
		}
		if err := q.CreatePreferences(ctx, id); err != nil {
			return fmt.Errorf("crear preferencias: %w", err)
		}
		if err := q.DeleteSetting(ctx, settingSetupHash); err != nil {
			return fmt.Errorf("borrar código inicial: %w", err)
		}
		user, err = q.GetUser(ctx, id)
		return err
	})
	if err != nil {
		s.log.Warn().Str("username", username).Str("resultado", core.CodeOf(err)).Msg("intento de configuración inicial")
		return nil, err
	}
	s.log.Info().Str("user_id", user.ID).Msg("owner creado")
	return &user, nil
}

// Login comprueba la contraseña y emite tokens. Toda rama sin contraseña válida
// calcula un hash y responde igual, para no revelar qué cuentas existen.
func (s *Service) Login(ctx context.Context, username, password string, dev DeviceInfo) (*TokenPair, error) {
	if err := validateDevice(&dev); err != nil {
		return nil, err
	}
	username = strings.TrimSpace(username)
	if utf8.RuneCountInString(username) > maxUsernameLength || len(password) > maxPasswordLength {
		return nil, s.loginFailed(username, dev, core.ErrInvalidCredentials())
	}
	if !s.accounts.Allow(strings.ToLower(username)) {
		return nil, s.loginFailed(username, dev, core.ErrRateLimited())
	}

	user, err := s.db.R.GetUserByUsername(ctx, username)
	found := err == nil
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("leer usuario: %w", err)
	}
	stored := s.dummy
	if found {
		stored = user.PasswordHash
	}
	ok, err := s.hasher.Verify(ctx, stored, password)
	if err != nil {
		return nil, err
	}
	if !found || !ok || user.IsActive == 0 {
		return nil, s.loginFailed(username, dev, core.ErrInvalidCredentials())
	}

	var pair *TokenPair
	err = s.db.InTx(ctx, func(q *sqlc.Queries) error {
		now := s.now()
		if err := q.TouchLastLogin(ctx, sqlc.TouchLastLoginParams{ID: user.ID, LastLoginAt: stringPtr(database.FormatTime(now))}); err != nil {
			return fmt.Errorf("registrar acceso: %w", err)
		}
		deviceID, err := s.upsertDevice(ctx, q, user.ID, dev, now)
		if err != nil {
			return err
		}
		pair, err = s.issue(ctx, q, user, deviceID, now)
		return err
	})
	if err != nil {
		return nil, err
	}
	s.log.Info().Str("user_id", user.ID).Str("device_id", pair.DeviceID).Str("ip", dev.IP).Msg("inicio de sesión")
	return pair, nil
}

func (s *Service) loginFailed(username string, dev DeviceInfo, apiErr *core.APIError) error {
	if utf8.RuneCountInString(username) > maxUsernameLength {
		username = "[demasiado largo]"
	}
	s.log.Warn().Str("username", username).Str("ip", dev.IP).Str("resultado", apiErr.Code).Msg("inicio de sesión fallido")
	return apiErr
}

// upsertDevice reutiliza la fila activa del cliente y retira sus tokens: cada
// dispositivo tiene una sola cadena de refresh.
func (s *Service) upsertDevice(ctx context.Context, q *sqlc.Queries, userID string, dev DeviceInfo, t time.Time) (string, error) {
	now := database.FormatTime(t)
	if dev.Identifier != "" {
		row, err := q.GetActiveDeviceByIdentifier(ctx, sqlc.GetActiveDeviceByIdentifierParams{
			UserID: userID, DeviceIdentifier: &dev.Identifier,
		})
		if err == nil {
			err = q.UpdateDeviceOnLogin(ctx, sqlc.UpdateDeviceOnLoginParams{
				ID: row.ID, DeviceName: dev.Name, Platform: dev.Platform,
				ClientVersion: nilIfEmpty(dev.ClientVersion), LastSeenAt: &now,
			})
			if err != nil {
				return "", fmt.Errorf("actualizar dispositivo: %w", err)
			}
			err = q.RevokeDeviceTokens(ctx, sqlc.RevokeDeviceTokensParams{DeviceID: row.ID, RevokedAt: &now})
			if err != nil {
				return "", fmt.Errorf("revocar tokens anteriores: %w", err)
			}
			return row.ID, nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return "", fmt.Errorf("buscar dispositivo: %w", err)
		}
	}

	id, err := database.NewID()
	if err != nil {
		return "", err
	}
	err = q.CreateDevice(ctx, sqlc.CreateDeviceParams{
		ID: id, UserID: userID, DeviceName: dev.Name, Platform: dev.Platform,
		ClientVersion: nilIfEmpty(dev.ClientVersion), DeviceIdentifier: nilIfEmpty(dev.Identifier), LastSeenAt: &now,
	})
	if err != nil {
		return "", fmt.Errorf("crear dispositivo: %w", err)
	}
	return id, nil
}

func (s *Service) issue(ctx context.Context, q *sqlc.Queries, user sqlc.User, deviceID string, now time.Time) (*TokenPair, error) {
	expires := now.Add(AccessTTL)
	access, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims{
		DeviceID: deviceID,
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   user.ID,
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(expires),
		},
	}).SignedString(s.secret)
	if err != nil {
		return nil, fmt.Errorf("firmar access token: %w", err)
	}

	refresh, err := randomToken()
	if err != nil {
		return nil, err
	}
	id, err := database.NewID()
	if err != nil {
		return nil, err
	}
	err = q.CreateRefreshToken(ctx, sqlc.CreateRefreshTokenParams{
		ID: id, DeviceID: deviceID, TokenHash: hashToken(refresh),
		ExpiresAt: database.FormatTime(now.Add(RefreshTTL)),
	})
	if err != nil {
		return nil, fmt.Errorf("guardar refresh token: %w", err)
	}
	return &TokenPair{AccessToken: access, RefreshToken: refresh, ExpiresAt: expires, User: user, DeviceID: deviceID}, nil
}

// Refresh rota el token. Uno ya rotado fuera de la ventana de reintento indica
// robo: se revoca el dispositivo y, por eso, la transacción se confirma aunque la
// llamada termine en error.
func (s *Service) Refresh(ctx context.Context, refreshToken string) (*TokenPair, error) {
	var pair *TokenPair
	var stolenFrom string
	err := s.db.InTx(ctx, func(q *sqlc.Queries) error {
		row, err := q.GetRefreshTokenByHash(ctx, hashToken(refreshToken))
		if errors.Is(err, sql.ErrNoRows) {
			return core.ErrInvalidCredentials()
		}
		if err != nil {
			return fmt.Errorf("leer refresh token: %w", err)
		}

		t := s.now()
		now := database.FormatTime(t)
		if row.RevokedAt != nil || row.ExpiresAt <= now {
			return core.ErrInvalidCredentials()
		}

		if row.RotatedAt != nil {
			rotated, err := database.ParseTime(*row.RotatedAt)
			if err != nil {
				return err
			}
			if t.Sub(rotated) > retryWindow {
				stolenFrom = row.DeviceID
				return devices.RevokeTx(ctx, q, row.DeviceID, now)
			}
			// El sucesor no llegó al cliente: se sustituye y queda uno solo vigente.
			err = q.RevokeUnrotatedDeviceTokens(ctx, sqlc.RevokeUnrotatedDeviceTokensParams{
				DeviceID: row.DeviceID, RevokedAt: &now,
			})
			if err != nil {
				return fmt.Errorf("revocar token sucesor: %w", err)
			}
		} else {
			err := q.MarkRefreshTokenRotated(ctx, sqlc.MarkRefreshTokenRotatedParams{ID: row.ID, RotatedAt: &now})
			if err != nil {
				return fmt.Errorf("rotar refresh token: %w", err)
			}
		}

		device, err := q.GetDevice(ctx, row.DeviceID)
		if err != nil {
			return fmt.Errorf("leer dispositivo: %w", err)
		}
		user, err := q.GetUser(ctx, device.UserID)
		if err != nil {
			return fmt.Errorf("leer usuario: %w", err)
		}
		if device.RevokedAt != nil || user.IsActive == 0 {
			return core.ErrInvalidCredentials()
		}

		if err := q.DeleteExpiredDeviceTokens(ctx, sqlc.DeleteExpiredDeviceTokensParams{DeviceID: device.ID, ExpiresAt: now}); err != nil {
			return fmt.Errorf("limpiar tokens: %w", err)
		}
		if err := q.TouchDevice(ctx, sqlc.TouchDeviceParams{ID: device.ID, LastSeenAt: &now}); err != nil {
			return fmt.Errorf("actualizar dispositivo: %w", err)
		}
		pair, err = s.issue(ctx, q, user, device.ID, t)
		return err
	})
	if err != nil {
		return nil, err
	}
	if stolenFrom != "" {
		s.log.Warn().Str("device_id", stolenFrom).Msg("refresh token reutilizado: dispositivo revocado")
		return nil, core.ErrDeviceRevoked()
	}
	return pair, nil
}

// Logout revoca el dispositivo que cierra la sesión.
func (s *Service) Logout(ctx context.Context, deviceID string) error {
	return s.db.InTx(ctx, func(q *sqlc.Queries) error {
		return devices.RevokeTx(ctx, q, deviceID, database.FormatTime(s.now()))
	})
}

// Authenticate valida el access token y consulta el estado actual del dispositivo
// y del usuario, de modo que revocar, desactivar o cambiar el rol surte efecto en
// la siguiente petición y no al caducar el token.
func (s *Service) Authenticate(ctx context.Context, token string) (authorization.Actor, error) {
	var c claims
	_, err := jwt.ParseWithClaims(token, &c, func(*jwt.Token) (any, error) { return s.secret, nil },
		jwt.WithValidMethods([]string{jwt.SigningMethodHS256.Alg()}),
		jwt.WithExpirationRequired(),
		jwt.WithTimeFunc(s.now),
	)
	if errors.Is(err, jwt.ErrTokenExpired) {
		return authorization.Actor{}, core.ErrAuthTokenExpired()
	}
	if err != nil {
		return authorization.Actor{}, core.ErrAuthRequired()
	}

	state, err := s.db.R.GetAuthState(ctx, sqlc.GetAuthStateParams{ID: c.DeviceID, UserID: c.Subject})
	if errors.Is(err, sql.ErrNoRows) {
		return authorization.Actor{}, core.ErrAuthRequired()
	}
	if err != nil {
		return authorization.Actor{}, fmt.Errorf("leer estado de sesión: %w", err)
	}
	if state.RevokedAt != nil {
		return authorization.Actor{}, core.ErrDeviceRevoked()
	}
	if state.IsActive == 0 {
		return authorization.Actor{}, core.ErrAuthRequired()
	}
	return authorization.Actor{
		UserID: c.Subject, DeviceID: c.DeviceID, Role: media.Role(state.Role), MaxAgeRating: state.MaxAgeRating,
	}, nil
}

// CurrentUser devuelve el usuario del actor.
func (s *Service) CurrentUser(ctx context.Context, actor authorization.Actor) (*sqlc.User, error) {
	user, err := s.db.R.GetUser(ctx, actor.UserID)
	if err != nil {
		return nil, fmt.Errorf("leer usuario: %w", err)
	}
	return &user, nil
}

// ChangePassword exige la contraseña actual y revoca los demás dispositivos del
// usuario, porque un cambio suele responder a una sospecha de acceso ajeno.
func (s *Service) ChangePassword(ctx context.Context, actor authorization.Actor, current, next string) error {
	if err := ValidatePassword(next); err != nil {
		return err
	}
	if !s.accounts.Allow("password:" + actor.UserID) {
		return core.ErrRateLimited()
	}
	user, err := s.CurrentUser(ctx, actor)
	if err != nil {
		return err
	}
	ok, err := s.hasher.Verify(ctx, user.PasswordHash, current)
	if err != nil {
		return err
	}
	if !ok {
		return core.ErrInvalidCredentials()
	}
	hash, err := s.hasher.Hash(ctx, next)
	if err != nil {
		return err
	}
	return s.db.InTx(ctx, func(q *sqlc.Queries) error {
		now := database.FormatTime(s.now())
		err := q.UpdatePassword(ctx, sqlc.UpdatePasswordParams{ID: actor.UserID, PasswordHash: hash, UpdatedAt: now})
		if err != nil {
			return fmt.Errorf("guardar contraseña: %w", err)
		}
		return devices.RevokeAllTx(ctx, q, actor.UserID, actor.DeviceID, now)
	})
}

// CreatePairingCode lo llama un dispositivo sin sesión, como una TV. Muestra el
// código en pantalla y espera con PollToken a que un usuario lo reclame.
func (s *Service) CreatePairingCode(ctx context.Context, dev DeviceInfo) (*PairingRequest, error) {
	if err := validateDevice(&dev); err != nil {
		return nil, err
	}
	code, err := randomPairingCode()
	if err != nil {
		return nil, err
	}
	poll, err := randomToken()
	if err != nil {
		return nil, err
	}
	id, err := database.NewID()
	if err != nil {
		return nil, err
	}

	t := s.now()
	expires := t.Add(PairingTTL)
	err = s.db.InTx(ctx, func(q *sqlc.Queries) error {
		if err := q.DeleteExpiredPairingCodes(ctx, database.FormatTime(t)); err != nil {
			return fmt.Errorf("limpiar códigos: %w", err)
		}
		return q.CreatePairingCode(ctx, sqlc.CreatePairingCodeParams{
			ID: id, CodeHash: hashToken(code), PollHash: hashToken(poll),
			DeviceName: dev.Name, Platform: dev.Platform,
			ClientVersion: nilIfEmpty(dev.ClientVersion), DeviceIdentifier: nilIfEmpty(dev.Identifier),
			ExpiresAt: database.FormatTime(expires),
		})
	})
	if err != nil {
		return nil, fmt.Errorf("crear código de vinculación: %w", err)
	}
	return &PairingRequest{Code: code, PollToken: poll, ExpiresAt: expires}, nil
}

// ClaimPairingCode asocia el código al usuario autenticado. Son 30 bits, así que
// además del límite por usuario hay uno global: varias cuentas no suman intentos.
func (s *Service) ClaimPairingCode(ctx context.Context, actor authorization.Actor, code string) (*PairingClaim, error) {
	if !s.accounts.Allow("pairing:"+actor.UserID) || !s.claimers.Allow(globalPairingKey) {
		return nil, core.ErrRateLimited()
	}
	var claim *PairingClaim
	err := s.db.InTx(ctx, func(q *sqlc.Queries) error {
		row, err := q.GetPairingCodeByCode(ctx, hashToken(normalizeCode(code)))
		if errors.Is(err, sql.ErrNoRows) {
			return core.ErrPairingInvalid()
		}
		if err != nil {
			return fmt.Errorf("leer código: %w", err)
		}
		now := database.FormatTime(s.now())
		if row.ExpiresAt <= now || row.ClaimedAt != nil {
			return core.ErrPairingInvalid()
		}
		err = q.ClaimPairingCode(ctx, sqlc.ClaimPairingCodeParams{
			ID: row.ID, ClaimedByUserID: &actor.UserID, ClaimedAt: &now,
		})
		if err != nil {
			return fmt.Errorf("reclamar código: %w", err)
		}
		claim = &PairingClaim{DeviceName: row.DeviceName, Platform: row.Platform}
		return nil
	})
	if err != nil {
		s.log.Warn().Str("user_id", actor.UserID).Str("resultado", core.CodeOf(err)).Msg("vinculación fallida")
		return nil, err
	}
	s.log.Info().Str("user_id", actor.UserID).Str("device_name", claim.DeviceName).Msg("código de vinculación reclamado")
	return claim, nil
}

// PollPairing entrega los tokens una sola vez. Devuelve nil mientras nadie haya
// reclamado el código.
func (s *Service) PollPairing(ctx context.Context, pollToken string) (*TokenPair, error) {
	var pair *TokenPair
	err := s.db.InTx(ctx, func(q *sqlc.Queries) error {
		row, err := q.GetPairingCodeByPoll(ctx, hashToken(pollToken))
		if errors.Is(err, sql.ErrNoRows) {
			return core.ErrPairingInvalid()
		}
		if err != nil {
			return fmt.Errorf("leer código: %w", err)
		}
		t := s.now()
		now := database.FormatTime(t)
		if row.ExpiresAt <= now || row.ConsumedAt != nil {
			return core.ErrPairingInvalid()
		}
		if row.ClaimedByUserID == nil {
			return nil
		}

		user, err := q.GetUser(ctx, *row.ClaimedByUserID)
		if err != nil {
			return fmt.Errorf("leer usuario: %w", err)
		}
		if user.IsActive == 0 {
			return core.ErrPairingInvalid()
		}
		if err := q.ConsumePairingCode(ctx, sqlc.ConsumePairingCodeParams{ID: row.ID, ConsumedAt: &now}); err != nil {
			return fmt.Errorf("consumir código: %w", err)
		}
		deviceID, err := s.upsertDevice(ctx, q, user.ID, DeviceInfo{
			Name: row.DeviceName, Platform: row.Platform,
			ClientVersion: deref(row.ClientVersion), Identifier: deref(row.DeviceIdentifier),
		}, t)
		if err != nil {
			return err
		}
		pair, err = s.issue(ctx, q, user, deviceID, t)
		return err
	})
	if err != nil {
		return nil, err
	}
	return pair, nil
}

// ValidateCredentials comprueba usuario y contraseña, y devuelve el usuario
// normalizado.
func ValidateCredentials(username, password string) (string, error) {
	username, err := ValidateUsername(username)
	if err != nil {
		return "", err
	}
	return username, ValidatePassword(password)
}

// ValidateUsername comprueba y normaliza un nombre de usuario.
func ValidateUsername(username string) (string, error) {
	username = strings.TrimSpace(username)
	n := utf8.RuneCountInString(username)
	if n < minUsernameLength || n > maxUsernameLength {
		return "", core.ErrValidation(fmt.Sprintf("El usuario debe tener entre %d y %d caracteres", minUsernameLength, maxUsernameLength))
	}
	if strings.ContainsFunc(username, unicode.IsControl) {
		return "", core.ErrValidation("El usuario contiene caracteres no válidos")
	}
	return username, nil
}

// ValidatePassword comprueba la longitud de una contraseña nueva.
func ValidatePassword(password string) error {
	if utf8.RuneCountInString(password) < minPasswordLength || len(password) > maxPasswordLength {
		return core.ErrValidation(fmt.Sprintf("La contraseña debe tener al menos %d caracteres", minPasswordLength))
	}
	return nil
}

func validateDevice(dev *DeviceInfo) error {
	dev.Name = strings.TrimSpace(dev.Name)
	dev.Platform = strings.ToLower(strings.TrimSpace(dev.Platform))
	if dev.Name == "" || dev.Platform == "" {
		return core.ErrValidation("Faltan el nombre o la plataforma del dispositivo")
	}
	for _, field := range []string{dev.Name, dev.Platform, dev.ClientVersion, dev.Identifier} {
		if utf8.RuneCountInString(field) > maxDeviceField {
			return core.ErrValidation("Los datos del dispositivo son demasiado largos")
		}
	}
	return nil
}

func normalizeCode(code string) string {
	return strings.ToUpper(strings.NewReplacer(" ", "", "-", "").Replace(code))
}

// NewCSRFToken genera el valor de la cookie de doble envío.
func NewCSRFToken() (string, error) {
	return randomToken()
}

func randomBytes(n int) ([]byte, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return nil, fmt.Errorf("generar valor aleatorio: %w", err)
	}
	return b, nil
}

func randomToken() (string, error) {
	b, err := randomBytes(secretBytes)
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// randomPairingCode no tiene sesgo: 256 es múltiplo de los 32 símbolos.
func randomPairingCode() (string, error) {
	b, err := randomBytes(pairingLength)
	if err != nil {
		return "", err
	}
	for i := range b {
		b[i] = pairingAlphabet[int(b[i])%len(pairingAlphabet)]
	}
	return string(b), nil
}

// hashToken usa SHA-256 porque los valores ya son aleatorios: Argon2 solo aporta
// frente a secretos elegidos por personas.
func hashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

func hashesEqual(a, b string) bool {
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

func stringPtr(s string) *string { return &s }

func nilIfEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
