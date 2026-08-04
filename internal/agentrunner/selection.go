package agentrunner

import (
	"errors"
	"fmt"
	"slices"
)

type ReasoningEffort string

const (
	EffortLow    ReasoningEffort = "low"
	EffortMedium ReasoningEffort = "medium"
	EffortHigh   ReasoningEffort = "high"
)

type Selection struct {
	Executor string          `yaml:"executor" json:"executor"`
	Model    string          `yaml:"model" json:"model"`
	Effort   ReasoningEffort `yaml:"effort" json:"effort"`
}

type ExecutorCapabilities struct {
	Name          string            `json:"name"`
	Models        []string          `json:"models"`
	Efforts       []ReasoningEffort `json:"efforts"`
	DefaultModel  string            `json:"default_model"`
	DefaultEffort ReasoningEffort   `json:"default_effort"`
}

var ErrInvalidSelection = errors.New("invalid agent selection")

func ValidateSelection(sel Selection, capability ExecutorCapabilities) error {
	if sel.Executor == "" || sel.Executor != capability.Name {
		return fmt.Errorf("%w: executor %q is not %q", ErrInvalidSelection, sel.Executor, capability.Name)
	}
	if len(capability.Models) == 0 {
		if sel.Model != "" {
			return fmt.Errorf("%w: executor %q does not accept a model", ErrInvalidSelection, sel.Executor)
		}
	} else if sel.Model == "" || !slices.Contains(capability.Models, sel.Model) {
		return fmt.Errorf("%w: model %q is not supported by executor %q", ErrInvalidSelection, sel.Model, sel.Executor)
	}
	if !slices.Contains(capability.Efforts, sel.Effort) {
		return fmt.Errorf("%w: effort %q is not supported by executor %q", ErrInvalidSelection, sel.Effort, sel.Executor)
	}
	return nil
}

func (c ExecutorCapabilities) DefaultSelection() Selection {
	return Selection{Executor: c.Name, Model: c.DefaultModel, Effort: c.DefaultEffort}
}
