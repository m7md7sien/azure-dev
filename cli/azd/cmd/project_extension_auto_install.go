// Copyright (c) Microsoft Corporation. All rights reserved.
// Licensed under the MIT License.

package cmd

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"maps"
	"slices"
	"strings"

	"github.com/azure/azure-dev/cli/azd/internal"
	"github.com/azure/azure-dev/cli/azd/pkg/extensions"
	"github.com/azure/azure-dev/cli/azd/pkg/input"
	"github.com/azure/azure-dev/cli/azd/pkg/ioc"
	"github.com/azure/azure-dev/cli/azd/pkg/output"
	"github.com/azure/azure-dev/cli/azd/pkg/output/ux"
	"github.com/azure/azure-dev/cli/azd/pkg/project"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

type projectExtensionRequirement struct {
	extension         *extensions.ExtensionMetadata
	versionPreference string
	explicit          bool
}

type resolvedExtensionDependency struct {
	parentId     string
	version      string
	capabilities []extensions.CapabilityType
	providers    []extensions.Provider
}

// extensionRef identifies an extension within a registry source, normalized for case-insensitive
// comparison. Dependency resolution tracks in-flight extensions by source and id because the same
// id can be published by more than one source, while resolved selections are keyed by id alone to
// match how installation reuses whatever is already installed.
type extensionRef struct {
	source string
	id     string
}

func newExtensionRef(source string, id string) extensionRef {
	return extensionRef{source: strings.ToLower(source), id: strings.ToLower(id)}
}

func projectCommandSupportsExtensionAutoInstall(cmd *cobra.Command) bool {
	if _, isExtensionCommand := cmd.Annotations["extension.id"]; isExtensionCommand {
		return false
	}

	path := getCommandPath(cmd)
	if len(path) == 0 {
		return false
	}

	switch path[0] {
	case "up", "provision", "deploy", "package", "restore", "down", "show", "monitor":
		return true
	case "infra":
		return len(path) > 1 && path[1] == "generate"
	case "env":
		return len(path) > 1 && path[1] == "refresh"
	default:
		return false
	}
}

// commandWillRun reports whether cobra will actually run cmd for the supplied arguments.
// rootCmd.Find resolves a command path but does not apply cobra's help short-circuit and does
// not reject invalid flags or arguments, so preflight must not treat every resolved command as
// one that executes. Installing extensions for `azd up --help` or `azd up --invalid` would
// mutate user state for an invocation that never runs.
func commandWillRun(cmd *cobra.Command, args []string) bool {
	if cmd.DisableFlagParsing {
		// Cobra forwards the arguments unparsed, so there is nothing to validate here.
		return true
	}

	flags := mirroredFlagSet(cmd)
	if err := flags.Parse(args); err != nil {
		return false
	}

	// Both flags make cobra render help or documentation instead of running the command.
	for _, name := range []string{"help", "docs"} {
		if !flags.Changed(name) {
			continue
		}
		if requested, err := flags.GetBool(name); err == nil && requested {
			return false
		}
	}

	return cmd.ValidateArgs(flags.Args()) == nil
}

// mirroredFlagSet returns a flag set that parses identically to cmd's flags but records values
// instead of binding them. Parsing cmd's own flag set twice would double-apply values for
// repeatable flags and trigger side effects in custom flag values (azd's --docs flag sets --help
// and swaps the command's help function from within Set), so preflight validation runs against
// this copy and leaves the real flag state for cobra to populate during execution.
func mirroredFlagSet(cmd *cobra.Command) *pflag.FlagSet {
	// Mirrors the start of cobra's execute(): InitDefaultHelpFlag merges the persistent flags
	// inherited from parent commands into cmd.Flags() before any parsing happens. Without it the
	// copy would be missing global flags such as --cwd and --debug and reject them as unknown.
	cmd.InitDefaultHelpFlag()

	flags := pflag.NewFlagSet(cmd.Name(), pflag.ContinueOnError)
	flags.SetOutput(io.Discard)
	flags.ParseErrorsAllowlist.UnknownFlags = cmd.FParseErrWhitelist.UnknownFlags
	cmd.Flags().VisitAll(func(flag *pflag.Flag) {
		flags.AddFlag(&pflag.Flag{
			Name:        flag.Name,
			Shorthand:   flag.Shorthand,
			Usage:       flag.Usage,
			Value:       &recordedFlagValue{valueType: flag.Value.Type()},
			DefValue:    flag.DefValue,
			NoOptDefVal: flag.NoOptDefVal,
		})
	})

	return flags
}

// recordedFlagValue accepts any input for a mirrored flag and records it verbatim so the value
// can be inspected without running the real flag's Set implementation.
type recordedFlagValue struct {
	valueType string
	value     string
}

func (v *recordedFlagValue) String() string {
	return v.value
}

func (v *recordedFlagValue) Set(value string) error {
	v.value = value
	return nil
}

func (v *recordedFlagValue) Type() string {
	return v.valueType
}

// providerLookup carries the state needed to decide which of the extensions publishing a provider
// can actually be installed to supply it. The maps are keyed by lowercase extension id, matching
// the case-insensitive comparison the extension manager applies to extension ids.
type providerLookup struct {
	// installed holds the currently installed extensions, keyed as the manager reports them.
	installed map[string]*extensions.Extension
	// resolvedDependencies holds extensions that installation will pull in as pack dependencies.
	resolvedDependencies map[string]resolvedExtensionDependency
	// requirementConflicts holds explicit requirements whose constrained version cannot supply
	// the provider, mapped to the conflict to report.
	requirementConflicts map[string]error
}

// providerCandidates partitions the extensions publishing a provider into those that can be
// installed and the reasons the remainder were excluded.
type providerCandidates struct {
	installable          []*extensions.ExtensionMetadata
	requirementConflicts map[string]error
	dependencyConflicts  map[string]resolvedExtensionDependency
}

// partition splits matches into installable candidates and the reasons for excluding the rest.
// Extensions that are already installed are dropped without a reason because an installed
// extension that does not supply the provider cannot be resolved by installing anything.
func (l providerLookup) partition(matches []*extensions.ExtensionMetadata) providerCandidates {
	candidates := providerCandidates{
		requirementConflicts: map[string]error{},
		dependencyConflicts:  map[string]resolvedExtensionDependency{},
	}

	for _, extension := range matches {
		extensionId := strings.ToLower(extension.Id)

		if conflict, hasConflict := l.requirementConflicts[extensionId]; hasConflict {
			candidates.requirementConflicts[extension.Id] = conflict
			continue
		}
		if dependency, isDependency := l.resolvedDependencies[extensionId]; isDependency {
			candidates.dependencyConflicts[extension.Id] = dependency
			continue
		}
		if _, isInstalled := installedExtensionById(l.installed, extension.Id); isInstalled {
			continue
		}

		candidates.installable = append(candidates.installable, extension)
	}

	return candidates
}

// conflictError reports why no candidate can supply the provider, or nil when the provider is
// simply unavailable. Conflicts are reported by lowest extension id so the message is stable.
func (c providerCandidates) conflictError(
	capability extensions.CapabilityType,
	provider string,
) error {
	if len(c.requirementConflicts) > 0 {
		extensionId := slices.Sorted(maps.Keys(c.requirementConflicts))[0]
		return c.requirementConflicts[extensionId]
	}

	if len(c.dependencyConflicts) > 0 {
		extensionId := slices.Sorted(maps.Keys(c.dependencyConflicts))[0]
		dependency := c.dependencyConflicts[extensionId]
		return fmt.Errorf(
			"extension %s requires dependency %s version %s, which does not provide %s %q",
			dependency.parentId,
			extensionId,
			dependency.version,
			capability,
			provider,
		)
	}

	return nil
}

// findExtensionForProvider selects an installable extension that supplies the given provider,
// prompting when more than one is available. It returns a nil extension and a nil error when no
// extension publishes the provider, and an error when one does but cannot be installed to supply it.
func findExtensionForProvider(
	ctx context.Context,
	console input.Console,
	extensionManager extensionAutoInstallManager,
	lookup providerLookup,
	capability extensions.CapabilityType,
	provider string,
) (*extensions.ExtensionMetadata, error) {
	matches, err := extensionManager.FindExtensions(ctx, &extensions.FilterOptions{
		Capability: capability,
		Provider:   provider,
	})
	if err != nil {
		return nil, fmt.Errorf("finding extension for provider %q: %w", provider, err)
	}

	candidates := lookup.partition(filterExtensionsForProvider(matches, capability, provider))
	if len(candidates.installable) == 0 {
		return nil, candidates.conflictError(capability, provider)
	}

	return promptForExtensionChoice(ctx, console, candidates.installable)
}

func uninstalledExtensionMatches(
	matches []*extensions.ExtensionMetadata,
	installed map[string]*extensions.Extension,
) []*extensions.ExtensionMetadata {
	return slices.DeleteFunc(slices.Clone(matches), func(extension *extensions.ExtensionMetadata) bool {
		_, isInstalled := installedExtensionById(installed, extension.Id)
		return isInstalled
	})
}

func installedExtensionById(
	installed map[string]*extensions.Extension,
	extensionId string,
) (*extensions.Extension, bool) {
	for installedId, extension := range installed {
		if strings.EqualFold(installedId, extensionId) {
			return extension, true
		}
	}
	return nil, false
}

// versionSatisfiesConstraint reports whether an already selected extension version satisfies a
// declared semver constraint. An empty constraint matches any version.
func versionSatisfiesConstraint(extensionId string, version string, constraint string) bool {
	if constraint == "" {
		return true
	}

	metadata := &extensions.ExtensionMetadata{
		Id:       extensionId,
		Versions: []extensions.ExtensionVersion{{Version: version}},
	}
	_, err := extensions.ResolveExtensionVersion(metadata, constraint, nil)
	return err == nil
}

func validateInstalledExtensionVersion(
	installed *extensions.Extension,
	versionPreference string,
) error {
	if versionSatisfiesConstraint(installed.Id, installed.Version, versionPreference) {
		return nil
	}

	return fmt.Errorf(
		"installed extension %s version %s does not satisfy constraint %q",
		installed.Id,
		installed.Version,
		versionPreference,
	)
}

func resolveExtensionRequirementDependencies(
	ctx context.Context,
	extensionManager extensionAutoInstallManager,
	requirements map[string]projectExtensionRequirement,
) (map[string]resolvedExtensionDependency, error) {
	resolved := map[string]resolvedExtensionDependency{}
	resolving := map[extensionRef]struct{}{}

	for _, requirement := range sortedProjectExtensionRequirements(requirements) {
		version, err := extensions.ResolveExtensionVersion(
			requirement.extension,
			requirement.versionPreference,
			nil,
		)
		if err != nil {
			return nil, fmt.Errorf("resolving required extension %s: %w", requirement.extension.Id, err)
		}

		key := newExtensionRef(requirement.extension.Source, requirement.extension.Id)
		resolving[key] = struct{}{}
		err = resolveExtensionDependencies(
			ctx,
			extensionManager,
			requirement.extension,
			version.Dependencies,
			resolved,
			resolving,
		)
		delete(resolving, key)
		if err != nil {
			return nil, err
		}
	}

	return resolved, nil
}

func resolveExtensionDependencies(
	ctx context.Context,
	extensionManager extensionAutoInstallManager,
	parent *extensions.ExtensionMetadata,
	dependencies []extensions.ExtensionDependency,
	resolved map[string]resolvedExtensionDependency,
	resolving map[extensionRef]struct{},
) error {
	for _, dependency := range dependencies {
		key := newExtensionRef(parent.Source, dependency.Id)
		if _, isResolving := resolving[key]; isResolving {
			return fmt.Errorf("dependency cycle detected involving extension %s", dependency.Id)
		}
		dependencyId := strings.ToLower(dependency.Id)
		if resolvedDependency, isResolved := resolved[dependencyId]; isResolved {
			// Another edge of the graph already selected a version. Installation resolves each edge
			// against whatever is installed at that point, so a version that satisfies only one edge
			// fails partway through installation instead of during preflight.
			if !versionSatisfiesConstraint(dependency.Id, resolvedDependency.version, dependency.Version) {
				return fmt.Errorf(
					"dependency %s version %s required by extension %s "+
						"does not satisfy constraint %q required by extension %s",
					dependency.Id,
					resolvedDependency.version,
					resolvedDependency.parentId,
					dependency.Version,
					parent.Id,
				)
			}
			continue
		}

		// Installation reuses a compatible installed dependency instead of replacing it with the registry selection.
		installedDependency, err := extensionManager.GetInstalled(extensions.FilterOptions{Id: dependency.Id})
		if err == nil && installedDependency != nil {
			if !versionSatisfiesConstraint(dependency.Id, installedDependency.Version, dependency.Version) {
				return fmt.Errorf(
					"installed dependency %s version %s does not satisfy constraint %q",
					dependency.Id,
					installedDependency.Version,
					dependency.Version,
				)
			}

			resolved[dependencyId] = resolvedExtensionDependency{
				parentId:     parent.Id,
				version:      installedDependency.Version,
				capabilities: installedDependency.Capabilities,
				providers:    installedDependency.Providers,
			}
			continue
		}

		matches, err := extensionManager.FindExtensions(ctx, &extensions.FilterOptions{
			Id:      dependency.Id,
			Version: dependency.Version,
			Source:  parent.Source,
		})
		if err != nil {
			return fmt.Errorf("finding dependency %s: %w", dependency.Id, err)
		}
		if len(matches) == 0 {
			return &extensions.DependencyNotFoundError{
				DependencyId: dependency.Id,
				ParentId:     parent.Id,
			}
		}
		if len(matches) > 1 {
			sources := make([]string, 0, len(matches))
			for _, match := range matches {
				sources = append(sources, match.Source)
			}
			slices.Sort(sources)
			sources = slices.Compact(sources)
			return &extensions.DependencyAmbiguousSourceError{
				DependencyId: dependency.Id,
				ParentId:     parent.Id,
				Sources:      sources,
			}
		}

		dependencyExtension := matches[0]
		version, err := extensions.ResolveExtensionVersion(dependencyExtension, dependency.Version, nil)
		if err != nil {
			return fmt.Errorf("resolving dependency %s: %w", dependency.Id, err)
		}
		resolved[dependencyId] = resolvedExtensionDependency{
			parentId:     parent.Id,
			version:      version.Version,
			capabilities: version.Capabilities,
			providers:    version.Providers,
		}

		resolving[key] = struct{}{}
		err = resolveExtensionDependencies(
			ctx,
			extensionManager,
			dependencyExtension,
			version.Dependencies,
			resolved,
			resolving,
		)
		delete(resolving, key)
		if err != nil {
			return err
		}
	}

	return nil
}

func extensionProvidesProvider(
	capabilities []extensions.CapabilityType,
	providers []extensions.Provider,
	capability extensions.CapabilityType,
	providerName string,
) bool {
	expectedType, hasProviderType := providerTypeForCapability(capability)
	if !hasProviderType || !slices.Contains(capabilities, capability) {
		return false
	}

	return slices.ContainsFunc(providers, func(provider extensions.Provider) bool {
		return provider.Type == expectedType && strings.EqualFold(provider.Name, providerName)
	})
}

func providerTypeForCapability(capability extensions.CapabilityType) (extensions.ProviderType, bool) {
	switch capability {
	case extensions.ServiceTargetProviderCapability:
		return extensions.ServiceTargetProviderType, true
	case extensions.ProvisioningProviderCapability:
		return extensions.ProvisioningProviderType, true
	default:
		return "", false
	}
}

func filterExtensionsForProvider(
	matches []*extensions.ExtensionMetadata,
	capability extensions.CapabilityType,
	providerName string,
) []*extensions.ExtensionMetadata {
	filtered := make([]*extensions.ExtensionMetadata, 0, len(matches))
	for _, extension := range matches {
		providerExtension := extensionForProvider(extension, capability, providerName)
		if len(providerExtension.Versions) > 0 {
			filtered = append(filtered, providerExtension)
		}
	}
	return filtered
}

func extensionVersionProvidesProvider(
	version *extensions.ExtensionVersion,
	capability extensions.CapabilityType,
	providerName string,
) bool {
	return extensionProvidesProvider(version.Capabilities, version.Providers, capability, providerName)
}

func resolvedDependencyProvidesProvider(
	dependency resolvedExtensionDependency,
	capability extensions.CapabilityType,
	providerName string,
) bool {
	return extensionProvidesProvider(
		dependency.capabilities,
		dependency.providers,
		capability,
		providerName,
	)
}

func extensionForProvider(
	extension *extensions.ExtensionMetadata,
	capability extensions.CapabilityType,
	providerName string,
) *extensions.ExtensionMetadata {
	filtered := *extension
	filtered.Versions = slices.DeleteFunc(slices.Clone(extension.Versions), func(version extensions.ExtensionVersion) bool {
		return !extensionVersionProvidesProvider(&version, capability, providerName)
	})
	return &filtered
}

func missingProjectExtensions(
	ctx context.Context,
	console input.Console,
	extensionManager extensionAutoInstallManager,
	projectConfig *project.ProjectConfig,
) ([]projectExtensionRequirement, error) {
	installed, err := extensionManager.ListInstalled()
	if err != nil {
		return nil, fmt.Errorf("listing installed extensions: %w", err)
	}

	requirements := map[string]projectExtensionRequirement{}
	if projectConfig.RequiredVersions != nil {
		for _, extensionId := range slices.Sorted(maps.Keys(projectConfig.RequiredVersions.Extensions)) {
			versionPreference := ""
			if constraint := projectConfig.RequiredVersions.Extensions[extensionId]; constraint != nil {
				versionPreference = *constraint
			}
			if installedExtension, isInstalled := installedExtensionById(installed, extensionId); isInstalled {
				if err := validateInstalledExtensionVersion(installedExtension, versionPreference); err != nil {
					return nil, err
				}
				continue
			}

			matches, err := extensionManager.FindExtensions(ctx, &extensions.FilterOptions{
				Id:      extensionId,
				Version: versionPreference,
			})
			if err != nil {
				return nil, fmt.Errorf("finding required extension %s: %w", extensionId, err)
			}
			if len(matches) == 0 {
				return nil, fmt.Errorf("required extension %s not found", extensionId)
			}

			extension, err := promptForExtensionChoice(ctx, console, matches)
			if err != nil {
				return nil, fmt.Errorf("selecting required extension %s: %w", extensionId, err)
			}

			requirements[extension.Id] = projectExtensionRequirement{
				extension:         extension,
				versionPreference: versionPreference,
				explicit:          true,
			}
		}
	}

	addProvider := func(capability extensions.CapabilityType, provider string) error {
		if provider == "" {
			return nil
		}

		requirementConflicts := map[string]error{}
		for _, extensionId := range slices.Sorted(maps.Keys(requirements)) {
			requirement := requirements[extensionId]
			selectedVersion, err := extensions.ResolveExtensionVersion(
				requirement.extension,
				requirement.versionPreference,
				nil,
			)
			if err != nil {
				return fmt.Errorf("resolving required extension %s: %w", extensionId, err)
			}
			if extensionVersionProvidesProvider(selectedVersion, capability, provider) {
				return nil
			}

			if len(extensionForProvider(requirement.extension, capability, provider).Versions) == 0 {
				continue
			}
			requirementConflicts[strings.ToLower(extensionId)] = fmt.Errorf(
				"required extension %s version %s does not provide %s %q",
				extensionId,
				selectedVersion.Version,
				capability,
				provider,
			)
		}

		resolvedDependencies, err := resolveExtensionRequirementDependencies(ctx, extensionManager, requirements)
		if err != nil {
			return err
		}
		for dependency := range maps.Values(resolvedDependencies) {
			if resolvedDependencyProvidesProvider(dependency, capability, provider) {
				return nil
			}
		}

		extension, err := findExtensionForProvider(
			ctx,
			console,
			extensionManager,
			providerLookup{
				installed:            installed,
				resolvedDependencies: resolvedDependencies,
				requirementConflicts: requirementConflicts,
			},
			capability,
			provider,
		)
		if err != nil || extension == nil {
			return err
		}
		if requirement, alreadyRequired := requirements[extension.Id]; alreadyRequired {
			requirement.extension = extensionForProvider(requirement.extension, capability, provider)
			if len(requirement.extension.Versions) == 0 {
				return fmt.Errorf(
					"required extension %s does not provide %s %q",
					extension.Id,
					capability,
					provider,
				)
			}
			requirements[extension.Id] = requirement
		} else {
			requirements[extension.Id] = projectExtensionRequirement{
				extension: extension,
			}
		}
		return nil
	}

	for _, serviceName := range slices.Sorted(maps.Keys(projectConfig.Services)) {
		if err := addProvider(
			extensions.ServiceTargetProviderCapability,
			string(projectConfig.Services[serviceName].Host),
		); err != nil {
			return nil, err
		}
	}

	for _, infra := range projectConfig.Infra.GetLayers() {
		if err := addProvider(extensions.ProvisioningProviderCapability, string(infra.Provider)); err != nil {
			return nil, err
		}
	}

	return sortedProjectExtensionRequirements(requirements), nil
}

func sortedProjectExtensionRequirements(
	requirements map[string]projectExtensionRequirement,
) []projectExtensionRequirement {
	result := slices.Collect(maps.Values(requirements))
	slices.SortFunc(result, func(a, b projectExtensionRequirement) int {
		if a.explicit != b.explicit {
			if a.explicit {
				return -1
			}
			return 1
		}
		return cmp.Compare(a.extension.Id, b.extension.Id)
	})

	return result
}

func tryAutoInstallProjectExtensions(
	ctx context.Context,
	rootContainer *ioc.NestedContainer,
	foundCmd *cobra.Command,
	args []string,
) (handled bool, installed bool, err error) {
	if !projectCommandSupportsExtensionAutoInstall(foundCmd) {
		return false, false, nil
	}

	if !commandWillRun(foundCmd, args) {
		return false, false, nil
	}

	var projectConfig *project.ProjectConfig
	if err := rootContainer.Resolve(&projectConfig); err != nil {
		log.Printf("skipping project extension auto-install: %v", err)
		return false, false, nil
	}

	var extensionManager *extensions.Manager
	if err := rootContainer.Resolve(&extensionManager); err != nil {
		return false, false, fmt.Errorf("resolving extension manager: %w", err)
	}
	var console input.Console
	if err := rootContainer.Resolve(&console); err != nil {
		return false, false, fmt.Errorf("resolving console: %w", err)
	}

	requirements, err := missingProjectExtensions(ctx, console, extensionManager, projectConfig)
	if err != nil {
		return false, false, err
	}
	if len(requirements) == 0 {
		return false, false, nil
	}

	installedAny := false
	for _, requirement := range requirements {
		installed, err := tryAutoInstallExtensionVersion(
			ctx,
			console,
			extensionManager,
			*requirement.extension,
			requirement.versionPreference,
		)
		if err != nil {
			return true, installedAny, err
		}
		installedAny = installedAny || installed
	}

	return true, installedAny, nil
}

func displayAutoInstallError(ctx context.Context, console input.Console, err error) {
	if suggestionErr, ok := errors.AsType[*internal.ErrorWithSuggestion](err); ok {
		console.Message(ctx, "")
		console.MessageUxItem(ctx, &ux.ErrorWithSuggestion{
			Err:        suggestionErr.Err,
			Message:    suggestionErr.Message,
			Suggestion: suggestionErr.Suggestion,
			Links:      suggestionErr.Links,
		})
		return
	}

	console.Message(ctx, output.WithErrorFormat("\nERROR: %s", err.Error()))
}
