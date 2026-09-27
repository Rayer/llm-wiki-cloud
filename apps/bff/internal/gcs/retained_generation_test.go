package gcs

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/rayer/llm-wiki-bff/internal/generation"
	store "github.com/rayer/llm-wiki-bff/internal/storage"
	"testing"
)

func TestRetainedGeneration(t *testing.T) {
	ctx := context.Background()
	c, b := newMemoryClient()
	for i, id := range []string{"generation-1", "generation-2"} {
		files := map[string]backendObject{"cache/concepts.jsonl": {Data: []byte(id), Generation: int64(11 + i)}}
		seedManifest(t, b, id, files)
		path, _ := generation.ArchivedManifestPath(id)
		b.put(projectObject(path), manifestBytes(t, id, files), 9, nil)
	}
	p, s, err := c.PinGeneration(ctx, "generation-1")
	if err != nil {
		t.Fatal(err)
	}
	if s.Manifest.GenerationID != "generation-1" || s.ManifestGeneration != 9 || s.ManifestSHA256 == "" {
		t.Fatalf("snapshot: %+v", s)
	}
	s.Manifest.Files[0].Generation = 999
	got, err := p.ReadFile(ctx, "cache/concepts.jsonl")
	if err != nil || string(got) != "generation-1" {
		t.Fatalf("generation-1: %q %v", got, err)
	}
	q, _, err := c.PinQueryGeneration(ctx, "generation-2")
	if err != nil {
		t.Fatal(err)
	}
	got, err = q.ReadFile(ctx, "cache/concepts.jsonl")
	if err != nil || string(got) != "generation-2" {
		t.Fatalf("generation-2: %q %v", got, err)
	}
	if p.ViewToken() == q.(store.ViewToken).ViewToken() || b.manifestReads != 0 {
		t.Fatal("token collision or read current")
	}
	other := c.WithScope("user", "other")
	archive, _ := generation.ArchivedManifestPath("generation-1")
	b.put(other.prefix()+"/"+archive, b.objects[projectObject(archive)].Data, 9, nil)
	otherPin, _, err := other.PinGeneration(ctx, "generation-1")
	if err != nil || otherPin.ViewToken() == p.ViewToken() {
		t.Fatalf("scope token: %v", err)
	}
	for _, scope := range [][2]string{{"user", "other"}, {"other", "project"}} {
		if p.WithScope(scope[0], scope[1]).view != nil {
			t.Fatal("cross scope pinned view")
		}
	}
	if p.WithScope("user", "project").view != p.view {
		t.Fatal("same scope lost view")
	}
	name := projectObject(generation.Prefix + "generation-1/cache/concepts.jsonl")
	for _, r := range b.requests {
		if r.Name == name && r.Generation != 11 {
			t.Fatal("unpinned read")
		}
	}
	for _, mode := range []string{"digest", "revision", "missing"} {
		switch mode {
		case "digest":
			b.put(name, []byte("generation-X"), 11, nil)
		case "revision":
			b.put(name, []byte("generation-1"), 12, nil)
		case "missing":
			delete(b.objects, name)
		}
		if _, err := p.ReadFile(ctx, "cache/concepts.jsonl"); !errors.Is(err, store.ErrDeclaredObjectUnavailable) {
			t.Fatalf("%s: %v", mode, err)
		}
	}
	for _, id := range []string{"../generation-1", "", "missing-generation"} {
		if _, _, err := c.PinGeneration(ctx, id); !errors.Is(err, store.ErrGenerationStateUnavailable) {
			t.Fatalf("archive %q: %v", id, err)
		}
	}
	b.put(projectObject(archive), manifestBytes(t, "generation-2", nil), 9, nil)
	if _, _, err := c.PinGeneration(ctx, "generation-1"); !errors.Is(err, store.ErrGenerationStateUnavailable) {
		t.Fatalf("wrong archive ID: %v", err)
	}
}

func sourceInventoryFixture(t *testing.T) (*Client, *memoryBackend, generation.SourceSnapshotManifest) {
	t.Helper()
	c, b := newMemoryClient()
	raw := []byte("retained source")
	idMap := []byte(`{"concept":{},"source":{"s1":"one"}}`)
	inventory := generation.SourceSnapshotManifest{SchemaVersion: generation.SourceSnapshotSchema, ContentGeneration: "generation-1", IDMapDigest: generation.Digest(idMap), SourceStatusDigest: generation.Digest([]byte("receipt")), Rows: []generation.SourceSnapshotRow{{StableID: "s1", RawPath: "raw/one.txt", ContentDigest: generation.Digest(raw), ObjectGeneration: 42}}}
	data, digest, err := generation.EncodeSourceSnapshot(inventory)
	if err != nil {
		t.Fatal(err)
	}
	c.view = &generationView{manifest: &generation.Manifest{GenerationID: "generation-1", SourceSnapshotDigest: digest, Files: []generation.File{{Path: "cache/id_map.json", SHA256: inventory.IDMapDigest, Size: int64(len(idMap)), Generation: 40}}}}
	b.put(projectObject(c.view.manifest.ObjectPath(c.view.manifest.Files[0])), idMap, 40, nil)
	path, _ := generation.SourceSnapshotPath(digest)
	b.put(projectObject(path), data, 41, nil)
	path, _ = generation.SourceBytesPath(inventory.Rows[0].ContentDigest)
	b.put(projectObject(path), raw, 42, nil)
	return c, b, inventory
}

func TestReadRetainedSourceSnapshot(t *testing.T) {
	c, b, want := sourceInventoryFixture(t)
	ctx := context.Background()
	got, err := c.ReadSourceSnapshot(ctx)
	if err != nil || got.ContentGeneration != want.ContentGeneration || len(got.Rows) != 1 {
		t.Fatalf("inventory: %+v %v", got, err)
	}
	raw, err := c.ReadSourceBytes(ctx, "s1")
	if err != nil || string(raw) != "retained source" {
		t.Fatalf("source: %q %v", raw, err)
	}
	last := b.requests[len(b.requests)-1]
	if last.Generation != 42 {
		t.Fatal("source read not pinned")
	}
	for _, mode := range []string{"digest", "revision", "missing"} {
		switch mode {
		case "digest":
			b.put(last.Name, []byte("corrupt"), 42, nil)
		case "revision":
			b.put(last.Name, []byte("retained source"), 43, nil)
		case "missing":
			delete(b.objects, last.Name)
		}
		if _, err := c.ReadSourceBytes(ctx, "s1"); !errors.Is(err, store.ErrDeclaredObjectUnavailable) {
			t.Fatalf("%s: %v", mode, err)
		}
	}
	if _, err := c.ReadSourceBytes(ctx, "missing"); !errors.Is(err, store.ErrObjectNotExist) {
		t.Fatalf("missing ID: %v", err)
	}
	if _, err := c.WithScope("user", "other").ReadSourceSnapshot(ctx); !errors.Is(err, store.ErrQueryGenerationUnpinned) {
		t.Fatalf("cross scope: %v", err)
	}
	if b.manifestReads != 0 {
		t.Fatal("read current")
	}
}

func TestReadRetainedSourceSnapshotFailsClosed(t *testing.T) {
	for _, tc := range []struct {
		name string
		want error
	}{{"no reference", ErrSourceSnapshotUnavailable}, {"missing object", ErrSourceSnapshotUnavailable}, {"digest", ErrSourceSnapshotInvalid}, {"generation", ErrSourceSnapshotInvalid}, {"id map", ErrSourceSnapshotInvalid}, {"raw path", ErrSourceSnapshotInvalid}, {"missing ID map", ErrSourceSnapshotUnavailable}} {
		t.Run(tc.name, func(t *testing.T) {
			c, b, inventory := sourceInventoryFixture(t)
			m := c.view.manifest
			path, _ := generation.SourceSnapshotPath(m.SourceSnapshotDigest)
			switch tc.name {
			case "no reference":
				m.SourceSnapshotDigest = ""
			case "missing object":
				delete(b.objects, projectObject(path))
			case "digest":
				b.put(projectObject(path), []byte("{}"), 41, nil)
			case "missing ID map":
				m.Files = nil
			default:
				switch tc.name {
				case "generation":
					inventory.ContentGeneration = "generation-2"
				case "id map":
					inventory.IDMapDigest = generation.Digest([]byte("wrong"))
				case "raw path":
					inventory.Rows[0].RawPath = "../invalid"
				}
				data, _ := json.Marshal(inventory)
				m.SourceSnapshotDigest = generation.Digest(data)
				path, _ = generation.SourceSnapshotPath(m.SourceSnapshotDigest)
				b.put(projectObject(path), data, 41, nil)
			}
			if _, err := c.ReadSourceSnapshot(context.Background()); !errors.Is(err, tc.want) {
				t.Fatalf("got %v want %v", err, tc.want)
			}
		})
	}
}
