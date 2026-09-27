package bridge

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"time"

	"github.com/openabstractions/abstraction-storage-over-local-stores/go/adapters/comfyui"
	"github.com/openabstractions/abstraction-storage-over-local-stores/go/adapters/lmstudio"
	"github.com/openabstractions/abstraction-storage-over-local-stores/go/source"
)

// LendDirectory is the one directory this provider creates inside an engine's
// own models directory. LM Studio reads the top level as the publisher, so
// every borrowed model is visibly borrowed in that engine's own list.
const LendDirectory = "openabstractions-lend"

// Engine is an installed engine a file can be lent to.
type Engine interface {
	// Name is the declared host name: lmstudio, lemonade, comfyui.
	Name() string
	// Root is the directory every lend of o to this engine sits under. LM
	// Studio and Lemonade answer the same directory for every object;
	// ComfyUI answers one directory per o's classified kind. A refusal here
	// is unsupported_engine.
	Root(o Object) (string, error)
	// Accept points the engine at root, for an engine that has to be told.
	// It runs on every lend, because the setting is the engine's, not this
	// provider's, and a person or a restart can change it back.
	Accept(root string) error
}

// LMStudio lends into LM Studio's own models folder, where it borrows the
// publisher/model/file layout that folder already is. LM Studio reads new
// subdirectories without being told; the 2026-09-22 experiment measured it
// listing a linked GGUF within 30 seconds with no restart. Every servable
// GGUF answers under the one root, regardless of kind.
type LMStudio struct{ Env source.Env }

func (LMStudio) Name() string { return "lmstudio" }

func (e LMStudio) Root(Object) (string, error) {
	d := lmstudio.Adapter{}.Discover(e.Env)
	for _, s := range d.Stores {
		if s.Name == "lmstudio" {
			if !s.Present {
				return "", fmt.Errorf("LM Studio's models folder %s does not exist", s.Locator)
			}
			return filepath.Join(s.Locator, LendDirectory), nil
		}
	}
	return "", errors.New("LM Studio has no models folder on this machine")
}

func (LMStudio) Accept(string) error { return nil }

// Lemonade lends into one directory this provider owns and tells Lemonade to
// scan it. Lemonade's own documentation names extra_models_dir as a setting
// changeable at runtime through POST /internal/set, so no person edits
// config.json for a lend; a Lemonade that will not take the setting is
// unsupported_engine.
type Lemonade struct {
	// Directory is the OA-managed directory of links, outside any store.
	Directory string
	// Base is where Lemonade answers, such as http://localhost:8000.
	Base string
	// Client is the HTTP client; nil uses one with a short timeout.
	Client *http.Client
}

func (Lemonade) Name() string { return "lemonade" }

func (e Lemonade) Root(Object) (string, error) {
	if e.Directory == "" {
		return "", errors.New("no lent-models directory is configured for Lemonade")
	}
	if e.Base == "" {
		return "", errors.New("no Lemonade server address is configured; extra_models_dir cannot be set without a person editing config.json")
	}
	return e.Directory, nil
}

// Accept writes extra_models_dir through Lemonade's own settings endpoint. A
// server that is not answering, or one that refuses the setting, leaves the
// lend refused: this provider never edits another program's configuration file.
func (e Lemonade) Accept(root string) error {
	body, err := json.Marshal(map[string]string{"extra_models_dir": filepath.ToSlash(root)})
	if err != nil {
		return err
	}
	client := e.Client
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	request, err := http.NewRequest(http.MethodPost, e.Base+"/internal/set", bytes.NewReader(body))
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := client.Do(request)
	if err != nil {
		return fmt.Errorf("Lemonade at %s did not answer: %w", e.Base, err)
	}
	defer response.Body.Close()
	//unchecked: detail is best-effort diagnostic text for the error message below; a partial read on failure still shows useful content
	detail, _ := io.ReadAll(io.LimitReader(response.Body, 512))
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("Lemonade at %s refused extra_models_dir: %s %s", e.Base, response.Status, bytes.TrimSpace(detail))
	}
	return nil
}

// ComfyUIFolders is the folder ComfyUI keeps under its models directory for
// each kind this provider classifies (MODELBRIDGE-L18). An object of a kind outside
// this table carries no target folder: the lend is unsupported_engine, the
// contract's own outcome for an engine a file cannot be placed with, and this
// provider adds none for it.
var ComfyUIFolders = map[string]string{
	"checkpoints":      "checkpoints",
	"loras":            "loras",
	"vae":              "vae",
	"clip":             "clip",
	"unet":             "unet",
	"diffusion_models": "diffusion_models",
	"controlnet":       "controlnet",
	"embeddings":       "embeddings",
	"upscale_models":   "upscale_models",
	"clip_vision":      "clip_vision",
	"text_encoders":    "text_encoders",
}

// ComfyUI lends into one folder per classified kind under its own models
// directory, and never into extra_model_paths.yaml. That file is ComfyUI's
// own, read once at its own startup: naming a new root there would need a
// restart to take effect, and writing it would be editing a file ComfyUI
// owns. A subdirectory placed inside a folder ComfyUI already scans is picked
// up on ComfyUI's own next listing, the same shape LM Studio's lend already
// uses (research/model-bridge/DECISION.md, dated section 2026-09-22).
type ComfyUI struct{ Env source.Env }

func (ComfyUI) Name() string { return "comfyui" }

func (e ComfyUI) Root(o Object) (string, error) {
	if o.Kind == "" {
		return "", fmt.Errorf("%s carries no classified ComfyUI kind; this provider will not guess one", o.File)
	}
	folder, known := ComfyUIFolders[o.Kind]
	if !known {
		return "", fmt.Errorf("ComfyUI keeps no folder for kind %q", o.Kind)
	}
	d := comfyui.Adapter{}.Discover(e.Env)
	for _, s := range d.Stores {
		if s.Name == "comfyui" {
			if !s.Present {
				return "", fmt.Errorf("ComfyUI's models folder %s does not exist", s.Locator)
			}
			return filepath.Join(s.Locator, folder, LendDirectory), nil
		}
	}
	return "", errors.New("ComfyUI has no models folder on this machine")
}

func (ComfyUI) Accept(string) error { return nil }
