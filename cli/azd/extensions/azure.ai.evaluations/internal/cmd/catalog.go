// Copyright (c) Microsoft Corporation. All rights reserved.
// Licensed under the MIT License.

package cmd

import (
	"fmt"
	"path/filepath"

	"azureaieval/internal/messages"
	"azureaieval/internal/project"

	"github.com/spf13/cobra"
)

// Generation writes the artifact and then names it in the configuration, so
// what it produced is referenceable without a hand edit. Only the catalogs are
// touched: which evals use the artifact is the author's decision, and `init` is
// the command that makes it.

// addDatasetToCatalog records a generated dataset in `datasets:`.
func addDatasetToCatalog(cmd *cobra.Command, evalDir string, ref *project.ArtifactRef) error {
	if ref == nil {
		return nil
	}
	return updateCatalog(cmd, evalDir, "dataset", ref, func(cfg *project.EvalConfig) bool {
		for i := range cfg.Datasets {
			if cfg.Datasets[i].Name == ref.Name {
				// Regeneration overwrites the file in place, so the entry only
				// changes when the artifact moved.
				if cfg.Datasets[i].File == ref.Source {
					return false
				}
				cfg.Datasets[i].File = ref.Source
				return true
			}
		}
		cfg.Datasets = append(cfg.Datasets, project.DatasetDecl{
			Name: ref.Name,
			File: ref.Source,
		})
		return true
	})
}

// addEvaluatorToCatalog records a generated evaluator in `evaluators:`.
func addEvaluatorToCatalog(cmd *cobra.Command, evalDir string, ref *project.ArtifactRef) error {
	if ref == nil {
		return nil
	}
	return updateCatalog(cmd, evalDir, "evaluator", ref, func(cfg *project.EvalConfig) bool {
		for i := range cfg.Evaluators {
			if cfg.Evaluators[i].Name == ref.Name {
				if cfg.Evaluators[i].Source == ref.Source {
					return false
				}
				cfg.Evaluators[i].Source = ref.Source
				return true
			}
		}
		cfg.Evaluators = append(cfg.Evaluators, project.EvaluatorDecl{
			Name:   ref.Name,
			Source: ref.Source,
		})
		return true
	})
}

// checkNameNotBehindAnInclude refuses a name the configuration already declares
// through a `$ref`.
//
// The editing read sees the directive, not the entry behind it, so an entry
// pulled in from its own file has no name here to match. Appending then writes a
// second entry with the same name, and the collision surfaces only on the next
// resolving read -- as a duplicate the author never wrote and cannot see in the
// file they are looking at.
//
// A configuration that will not resolve is left to the commands that resolve it:
// failing a generate over an unrelated broken include would be its own surprise.
func checkNameNotBehindAnInclude(evalDir string, asWritten *project.EvalConfig, kind, name string) error {
	if catalogDeclares(asWritten, kind, name) {
		return nil
	}
	resolved, err := project.OpenEvalConfig(evalDir)
	if err != nil || resolved == nil {
		return nil
	}
	if catalogDeclares(resolved, kind, name) {
		return messages.CatalogNameBehindAnInclude(kind, name)
	}
	return nil
}

// catalogDeclares reports whether the configuration names this artifact.
func catalogDeclares(cfg *project.EvalConfig, kind, name string) bool {
	if cfg == nil {
		return false
	}
	if kind == "dataset" {
		_, ok := cfg.DatasetDeclaration(name)
		return ok
	}
	_, ok := cfg.EvaluatorDeclaration(name)
	return ok
}

// updateCatalog applies a change to the configuration and writes it back.
//
// A missing configuration is created holding only the catalog. `generate` runs
// before `init` on the golden path, and a downloaded artifact nobody recorded
// is the one state that goes stale. The file it creates has no evals and no
// azure.yaml entry, so it stays inert until init wires one.
func updateCatalog(
	cmd *cobra.Command,
	evalDir string,
	kind string,
	ref *project.ArtifactRef,
	apply func(*project.EvalConfig) bool,
) error {
	// Held across the read and the write: two generates adding different
	// entries would otherwise both read the same state, and the second write
	// would drop the first one's entry while reporting success.
	unlock, err := project.LockEvalConfig(cmd.Context(), evalDir)
	if err != nil {
		return err
	}
	defer unlock()

	cfg, err := project.OpenEvalConfigForEdit(evalDir)
	if err != nil {
		return err
	}
	created := cfg == nil
	if created {
		cfg = &project.EvalConfig{}
	}
	if err := checkNameNotBehindAnInclude(evalDir, cfg, kind, ref.Name); err != nil {
		return err
	}
	if !apply(cfg) {
		return nil
	}

	if err := project.SaveEvalConfig(evalDir, cfg); err != nil {
		return err
	}
	if !isJSON(cmd) {
		// Resolved, not the current name: SaveEvalConfig writes back over a
		// legacy file when that is what the project has, and the line has to
		// name the file it actually wrote.
		resolved, err := project.ResolveEvalConfigPath(evalDir)
		if err != nil {
			return err
		}
		path := filepath.ToSlash(resolved)
		if created {
			fmt.Fprint(cmd.OutOrStdout(), messages.CreatedCatalogFile(path))
		}
		fmt.Fprint(cmd.OutOrStdout(),
			messages.AddedToCatalog(kind, describeArtifact(ref), path))
	}
	return nil
}

// describeArtifact names what was recorded, with the published version when the
// job reported one, so a reader can pin it without going to look.
//
// Single-quoted to match the spec's transcripts; the surrounding Done: lines
// carry bare values, but a dataset name can hold a space and these cannot.
func describeArtifact(ref *project.ArtifactRef) string {
	return messages.ArtifactDescription(ref.Name, ref.Version)
}
