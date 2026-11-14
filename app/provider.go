package app

import (
	"encoding/json"
	"strings"

	domain "github.com/slidebolt/sb-domain"
	media "github.com/slidebolt/sb-media-sdk"
)

func (a *App) Browse(media.CatalogRequest) media.CatalogPage {
	return media.CatalogPage{Items: []media.MediaItem{}}
}

func (a *App) Artwork(media.ArtworkRequest) media.ArtworkReply {
	return media.ArtworkReply{Error: "Frigate artwork is exposed through camera snapshot references"}
}

func (a *App) Guide(media.GuideRequest) media.GuidePage {
	return media.GuidePage{Error: "Frigate does not provide guide data"}
}

func (a *App) Resolve(req media.ResolveRequest) media.ResolveReply {
	if req.Source.Kind != "live" || req.Source.Entity == "" {
		return media.ResolveReply{Error: "Frigate resolves live entity sources"}
	}
	parts := strings.Split(req.Source.Entity, ".")
	if len(parts) != 3 || parts[0] != PluginID {
		return media.ResolveReply{Error: "invalid Frigate source entity"}
	}
	raw, err := a.store.Get(domain.EntityKey{Plugin: parts[0], DeviceID: parts[1], ID: parts[2]})
	if err != nil {
		return media.ResolveReply{Error: err.Error()}
	}
	var entity domain.Entity
	if err := json.Unmarshal(raw, &entity); err != nil {
		return media.ResolveReply{Error: err.Error()}
	}
	source, ok := entity.State.(domain.MediaSource)
	if !ok || entity.Type != "media_source" {
		return media.ResolveReply{Error: "entity is not a media source"}
	}
	if !source.Available {
		return media.ResolveReply{Error: "media source is unavailable"}
	}
	return media.ResolveReply{URL: source.URL, Kind: source.Kind, Format: source.Format}
}
