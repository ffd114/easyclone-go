// Package installer wires the phases together into the top-level flow of
// SPEC §4: load config, set up Moodle, process every repository in order.
// Everything runs strictly sequentially, and the first error aborts the run.
package installer

import (
	"github.com/ffd114/easyclone-go/internal/config"
	"github.com/ffd114/easyclone-go/internal/moodle"
	"github.com/ffd114/easyclone-go/internal/repo"
)

// Run executes the whole build for the manifest at configPath.
func Run(configPath string) error {
	cfg, err := config.Load(configPath)
	if err != nil {
		return err
	}
	if err := moodle.Setup(cfg); err != nil {
		return err
	}
	for i := range cfg.Repositories {
		if err := repo.Process(cfg, &cfg.Repositories[i]); err != nil {
			return err
		}
	}
	return nil
}
