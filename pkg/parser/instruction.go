package parser

import (
	"github.com/imagin/imagin/internal/types"
)

// instructionKeywords maps uppercase keywords to their InstructionType.
// Using a map gives O(1) dispatch — avoids switch/case chains.
var instructionKeywords = map[string]types.InstructionType{
	"FROM":        types.InstructionFrom,
	"RUN":         types.InstructionRun,
	"COPY":        types.InstructionCopy,
	"ADD":         types.InstructionAdd,
	"CMD":         types.InstructionCmd,
	"ENTRYPOINT":  types.InstructionEntrypoint,
	"ENV":         types.InstructionEnv,
	"EXPOSE":      types.InstructionExpose,
	"VOLUME":      types.InstructionVolume,
	"WORKDIR":     types.InstructionWorkdir,
	"USER":        types.InstructionUser,
	"ARG":         types.InstructionArg,
	"LABEL":       types.InstructionLabel,
	"SHELL":       types.InstructionShell,
	"STOPSIGNAL":  types.InstructionStopsignal,
	"HEALTHCHECK": types.InstructionHealthcheck,
	"ONBUILD":     types.InstructionOnbuild,
	"MAINTAINER":  types.InstructionMaintainer,
}

// LookupInstruction returns the InstructionType for the given keyword.
// ok is false if the keyword is not a valid Dockerfile instruction.
func LookupInstruction(keyword string) (types.InstructionType, bool) {
	t, ok := instructionKeywords[keyword]
	return t, ok
}

// IsMetadataOnly returns true if the instruction only affects image
// configuration (no filesystem changes → empty layer).
func IsMetadataOnly(t types.InstructionType) bool {
	switch t {
	case types.InstructionEnv,
		types.InstructionExpose,
		types.InstructionVolume,
		types.InstructionWorkdir,
		types.InstructionUser,
		types.InstructionCmd,
		types.InstructionEntrypoint,
		types.InstructionLabel,
		types.InstructionShell,
		types.InstructionStopsignal,
		types.InstructionHealthcheck,
		types.InstructionArg,
		types.InstructionMaintainer,
		types.InstructionOnbuild:
		return true
	default:
		return false
	}
}
