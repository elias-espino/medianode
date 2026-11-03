// Package tmdb adapta The Movie Database (API v3) a metadata.Provider.
package tmdb

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"golang.org/x/time/rate"

	"medianode/internal/media"
	"medianode/internal/metadata"
)

// Valores de producción. Las pruebas los sustituyen por un servidor local.
const (
	DefaultBaseURL  = "https://api.themoviedb.org/3"
	DefaultImageURL = "https://image.tmdb.org/t/p"

	maxResponse    = 4 << 20
	requestTimeout = 20 * time.Second
	maxCast        = 15
)

// Tamaños de imagen pedidos a TMDB. Bastan para un panel y un cliente de TV
// sin descargar los originales, que pesan varios megas.
var imageSizes = map[media.ImageType]string{
	media.ImagePoster:    "w780",
	media.ImageBackdrop:  "w1280",
	media.ImageThumbnail: "w780",
	media.ImageLogo:      "w500",
}

// Client es el adaptador. Language es una etiqueta como «es-ES»; su región
// elige la clasificación por edades, con Estados Unidos como respaldo.
type Client struct {
	BaseURL  string
	ImageURL string
	Key      string
	Language string
	HTTP     *http.Client
	limiter  *rate.Limiter
}

// New crea el cliente con un ritmo prudente frente al límite de TMDB.
func New(key, language string) *Client {
	return &Client{
		BaseURL:  DefaultBaseURL,
		ImageURL: DefaultImageURL,
		Key:      key,
		Language: language,
		HTTP:     &http.Client{Timeout: requestTimeout},
		limiter:  rate.NewLimiter(rate.Limit(10), 10),
	}
}

// Name implementa metadata.Provider.
func (c *Client) Name() string { return "tmdb" }

func (c *Client) region() string {
	_, region, _ := strings.Cut(c.Language, "-")
	return strings.ToUpper(region)
}

// get pide path y decodifica la respuesta en v.
func (c *Client) get(ctx context.Context, path string, params url.Values, v any) error {
	if c.limiter != nil {
		if err := c.limiter.Wait(ctx); err != nil {
			return err
		}
	}
	if params == nil {
		params = url.Values{}
	}
	params.Set("language", c.Language)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.BaseURL+path, nil)
	if err != nil {
		return err
	}
	// Un token de lectura v4 es un JWT; una clave v3 va como parámetro.
	if strings.Count(c.Key, ".") == 2 {
		req.Header.Set("Authorization", "Bearer "+c.Key)
	} else {
		params.Set("api_key", c.Key)
	}
	req.URL.RawQuery = params.Encode()
	req.Header.Set("Accept", "application/json")

	resp, err := c.HTTP.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		// La URL lleva la clave: el error no la incluye.
		return fmt.Errorf("%w: %s", metadata.ErrUnavailable, describe(err))
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusOK:
	case resp.StatusCode == http.StatusUnauthorized:
		return metadata.ErrUnauthorized
	case resp.StatusCode == http.StatusNotFound:
		return metadata.ErrNotFound
	case resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500:
		return fmt.Errorf("%w: HTTP %d", metadata.ErrUnavailable, resp.StatusCode)
	default:
		return fmt.Errorf("TMDB respondió HTTP %d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxResponse+1))
	if err != nil {
		return fmt.Errorf("%w: %s", metadata.ErrUnavailable, describe(err))
	}
	if len(data) > maxResponse {
		return errors.New("respuesta de TMDB demasiado grande")
	}
	if err := json.Unmarshal(data, v); err != nil {
		return fmt.Errorf("respuesta de TMDB no válida: %w", err)
	}
	return nil
}

// describe quita la URL de un error de red, porque contiene la clave.
func describe(err error) string {
	var uerr *url.Error
	if errors.As(err, &uerr) {
		return uerr.Err.Error()
	}
	return err.Error()
}

func (c *Client) image(t media.ImageType, path string) string {
	if path == "" {
		return ""
	}
	return c.ImageURL + "/" + imageSizes[t] + path
}

type searchResponse struct {
	Results []struct {
		ID            int64  `json:"id"`
		Title         string `json:"title"`
		Name          string `json:"name"`
		OriginalTitle string `json:"original_title"`
		OriginalName  string `json:"original_name"`
		ReleaseDate   string `json:"release_date"`
		FirstAirDate  string `json:"first_air_date"`
		Overview      string `json:"overview"`
		PosterPath    string `json:"poster_path"`
	} `json:"results"`
}

// Search implementa metadata.Provider. Si el año no da resultados se busca sin
// él: la fecha de TMDB puede ser la de otro país y diferir en uno.
func (c *Client) Search(ctx context.Context, q metadata.Query) ([]metadata.Match, error) {
	path, yearParam := "/search/movie", "year"
	if q.Kind == media.ItemSeries {
		path, yearParam = "/search/tv", "first_air_date_year"
	}
	params := url.Values{"query": {q.Title}, "include_adult": {"false"}}
	if q.Year != nil {
		params.Set(yearParam, strconv.FormatInt(*q.Year, 10))
	}
	var resp searchResponse
	if err := c.get(ctx, path, params, &resp); err != nil {
		return nil, err
	}
	if len(resp.Results) == 0 && q.Year != nil {
		params.Del(yearParam)
		if err := c.get(ctx, path, params, &resp); err != nil {
			return nil, err
		}
	}
	out := make([]metadata.Match, 0, len(resp.Results))
	for _, r := range resp.Results {
		m := metadata.Match{
			ExternalID:    strconv.FormatInt(r.ID, 10),
			Title:         first(r.Title, r.Name),
			OriginalTitle: first(r.OriginalTitle, r.OriginalName),
			Year:          year(first(r.ReleaseDate, r.FirstAirDate)),
			Overview:      r.Overview,
			PosterURL:     c.image(media.ImagePoster, r.PosterPath),
		}
		out = append(out, m)
	}
	return out, nil
}

type credit struct {
	ID        int64  `json:"id"`
	Name      string `json:"name"`
	Character string `json:"character"`
	Order     int64  `json:"order"`
	Job       string `json:"job"`
}

type detailsResponse struct {
	ID               int64  `json:"id"`
	Title            string `json:"title"`
	Name             string `json:"name"`
	OriginalTitle    string `json:"original_title"`
	OriginalName     string `json:"original_name"`
	Overview         string `json:"overview"`
	OriginalLanguage string `json:"original_language"`
	ReleaseDate      string `json:"release_date"`
	FirstAirDate     string `json:"first_air_date"`
	PosterPath       string `json:"poster_path"`
	BackdropPath     string `json:"backdrop_path"`
	Genres           []struct {
		Name string `json:"name"`
	} `json:"genres"`
	CreatedBy []credit `json:"created_by"`
	Credits   struct {
		Cast []credit `json:"cast"`
		Crew []credit `json:"crew"`
	} `json:"credits"`
	ReleaseDates struct {
		Results []struct {
			Country string `json:"iso_3166_1"`
			Dates   []struct {
				Certification string `json:"certification"`
				Type          int    `json:"type"`
			} `json:"release_dates"`
		} `json:"results"`
	} `json:"release_dates"`
	ContentRatings struct {
		Results []struct {
			Country string `json:"iso_3166_1"`
			Rating  string `json:"rating"`
		} `json:"results"`
	} `json:"content_ratings"`
	ExternalIDs struct {
		IMDB string `json:"imdb_id"`
		TVDB *int64 `json:"tvdb_id"`
	} `json:"external_ids"`
	Images struct {
		Logos []struct {
			FilePath string `json:"file_path"`
			Language string `json:"iso_639_1"`
		} `json:"logos"`
	} `json:"images"`
}

// Details implementa metadata.Provider con una sola petición por elemento.
func (c *Client) Details(ctx context.Context, externalID string, kind media.ItemType) (*metadata.Details, error) {
	if _, err := strconv.ParseInt(externalID, 10, 64); err != nil {
		return nil, metadata.ErrNotFound
	}
	path, appendTo := "/movie/", "credits,release_dates,external_ids,images"
	if kind == media.ItemSeries {
		path, appendTo = "/tv/", "credits,content_ratings,external_ids,images"
	}
	lang, _, _ := strings.Cut(c.Language, "-")
	params := url.Values{
		"append_to_response":     {appendTo},
		"include_image_language": {lang + ",en,null"},
	}
	var r detailsResponse
	if err := c.get(ctx, path+externalID, params, &r); err != nil {
		return nil, err
	}

	d := &metadata.Details{
		ExternalID:       strconv.FormatInt(r.ID, 10),
		Title:            first(r.Title, r.Name),
		OriginalTitle:    first(r.OriginalTitle, r.OriginalName),
		Overview:         r.Overview,
		OriginalLanguage: r.OriginalLanguage,
		ExternalIDs:      map[string]string{},
		Images:           map[media.ImageType]string{},
	}
	date := first(r.ReleaseDate, r.FirstAirDate)
	d.Year = year(date)
	if d.Year != nil {
		d.ReleaseDate = &date
	}
	for _, g := range r.Genres {
		d.Genres = append(d.Genres, g.Name)
	}
	if r.ExternalIDs.IMDB != "" {
		d.ExternalIDs["imdb"] = r.ExternalIDs.IMDB
	}
	if r.ExternalIDs.TVDB != nil {
		d.ExternalIDs["tvdb"] = strconv.FormatInt(*r.ExternalIDs.TVDB, 10)
	}
	if u := c.image(media.ImagePoster, r.PosterPath); u != "" {
		d.Images[media.ImagePoster] = u
	}
	if u := c.image(media.ImageBackdrop, r.BackdropPath); u != "" {
		d.Images[media.ImageBackdrop] = u
	}
	if logo := pickLogo(r, lang); logo != "" {
		d.Images[media.ImageLogo] = c.image(media.ImageLogo, logo)
	}

	d.People = people(r)
	ratings := map[string]string{}
	for _, rr := range r.ReleaseDates.Results {
		ratings[rr.Country] = movieCertification(rr.Dates)
	}
	for _, cr := range r.ContentRatings.Results {
		ratings[cr.Country] = cr.Rating
	}
	d.AgeRating, d.MinAge = pickRating(ratings, c.region())
	return d, nil
}

// movieCertification prefiere el estreno en cines (tipo 3) a otras ediciones.
func movieCertification(dates []struct {
	Certification string `json:"certification"`
	Type          int    `json:"type"`
}) string {
	best := ""
	for _, d := range dates {
		if d.Certification == "" {
			continue
		}
		if d.Type == 3 {
			return d.Certification
		}
		if best == "" {
			best = d.Certification
		}
	}
	return best
}

func pickLogo(r detailsResponse, lang string) string {
	for _, want := range []string{lang, "en", ""} {
		for _, l := range r.Images.Logos {
			if l.Language == want {
				return l.FilePath
			}
		}
	}
	return ""
}

func people(r detailsResponse) []metadata.Person {
	var out []metadata.Person
	for _, p := range r.CreatedBy {
		out = append(out, metadata.Person{ExternalID: personID(p.ID), Name: p.Name, Role: metadata.RoleCreator})
	}
	for _, p := range r.Credits.Cast {
		if len(out) >= maxCast+len(r.CreatedBy) {
			break
		}
		out = append(out, metadata.Person{
			ExternalID: personID(p.ID), Name: p.Name, Role: metadata.RoleActor, Character: p.Character, Order: p.Order,
		})
	}
	var order int64
	for _, p := range r.Credits.Crew {
		role := ""
		switch p.Job {
		case "Director":
			role = metadata.RoleDirector
		case "Screenplay", "Writer":
			role = metadata.RoleWriter
		default:
			continue
		}
		out = append(out, metadata.Person{ExternalID: personID(p.ID), Name: p.Name, Role: role, Order: order})
		order++
	}
	return out
}

func personID(id int64) string { return strconv.FormatInt(id, 10) }

type seasonResponse struct {
	SeasonNumber int64  `json:"season_number"`
	Name         string `json:"name"`
	Overview     string `json:"overview"`
	AirDate      string `json:"air_date"`
	PosterPath   string `json:"poster_path"`
	Episodes     []struct {
		EpisodeNumber int64  `json:"episode_number"`
		Name          string `json:"name"`
		Overview      string `json:"overview"`
		AirDate       string `json:"air_date"`
		StillPath     string `json:"still_path"`
	} `json:"episodes"`
}

// Season implementa metadata.Provider.
func (c *Client) Season(ctx context.Context, seriesID string, number int64) (*metadata.Season, error) {
	if _, err := strconv.ParseInt(seriesID, 10, 64); err != nil {
		return nil, metadata.ErrNotFound
	}
	var r seasonResponse
	path := "/tv/" + seriesID + "/season/" + strconv.FormatInt(number, 10)
	if err := c.get(ctx, path, nil, &r); err != nil {
		return nil, err
	}
	s := &metadata.Season{
		Number: r.SeasonNumber, Title: r.Name, Overview: r.Overview, AirDate: date(r.AirDate),
		PosterURL: c.image(media.ImagePoster, r.PosterPath),
	}
	for _, e := range r.Episodes {
		s.Episodes = append(s.Episodes, metadata.Episode{
			Number: e.EpisodeNumber, Title: e.Name, Overview: e.Overview, AirDate: date(e.AirDate),
			StillURL: c.image(media.ImageThumbnail, e.StillPath),
		})
	}
	return s, nil
}

func first(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// year saca el año de una fecha YYYY-MM-DD.
func year(d string) *int64 {
	if len(d) < 4 {
		return nil
	}
	y, err := strconv.ParseInt(d[:4], 10, 64)
	if err != nil || y < 1800 {
		return nil
	}
	return &y
}

func date(d string) *string {
	if year(d) == nil {
		return nil
	}
	return &d
}
