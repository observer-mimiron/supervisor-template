package eval

import "fmt"

// EvaluationProfile describes how evidence was produced. It is metadata owned
// by the evaluation entrypoint, not something inferred from model output.
type EvaluationProfile struct {
	EvaluationPlane string  `json:"evaluation_plane"`
	Scenario        string  `json:"scenario"`
	ModelProvider   *string `json:"model_provider"`
	ModelName       *string `json:"model_name"`
	Executor        *string `json:"executor"`
	Verifier        *string `json:"verifier"`
	NetworkMode     string  `json:"network_mode"`
	Tier            string  `json:"tier"`
}

const (
	PlaneRuntime = "runtime"
	PlaneCoding  = "coding"

	ScenarioRuntimeFake = "runtime-fake"
	ScenarioRuntimeReal = "runtime-real"
	ScenarioCodingFake  = "coding-fake"
	ScenarioCodingReal  = "coding-real"

	ProviderFake     = "fake"
	ProviderDeepSeek = "deepseek"

	ExecutorLocalFake = "local-fake"
	ExecutorIsolated  = "isolated"

	VerifierDeterministic  = "deterministic"
	VerifierHiddenIsolated = "hidden-isolated"

	NetworkDisabled  = "disabled"
	NetworkAllowlist = "allowlist"
	NetworkEnabled   = "enabled"

	TierPR      = "pr"
	TierNightly = "nightly"
	TierRelease = "release"
)

func String(value string) *string { return &value }

func RuntimeFakeProfile(tier string) EvaluationProfile {
	if tier == "" {
		tier = TierPR
	}
	return EvaluationProfile{
		EvaluationPlane: PlaneRuntime,
		Scenario:        ScenarioRuntimeFake,
		ModelProvider:   String(ProviderFake),
		ModelName:       String("fake-supervisor@1"),
		Executor:        String(ExecutorLocalFake),
		Verifier:        String(VerifierDeterministic),
		NetworkMode:     NetworkDisabled,
		Tier:            tier,
	}
}

func (p EvaluationProfile) Validate() error {
	if p.EvaluationPlane != PlaneRuntime && p.EvaluationPlane != PlaneCoding {
		return fmt.Errorf("evaluation_setup_error: invalid evaluation_plane %q", p.EvaluationPlane)
	}
	switch p.Scenario {
	case ScenarioRuntimeFake:
		if p.EvaluationPlane != PlaneRuntime || value(p.ModelProvider) != ProviderFake || value(p.Executor) != ExecutorLocalFake || value(p.Verifier) != VerifierDeterministic || p.NetworkMode != NetworkDisabled {
			return fmt.Errorf("evaluation_setup_error: runtime-fake profile mismatch")
		}
	case ScenarioRuntimeReal:
		if p.EvaluationPlane != PlaneRuntime || value(p.ModelProvider) == "" || value(p.ModelProvider) == ProviderFake || value(p.ModelName) == "" || p.NetworkMode == NetworkDisabled {
			return fmt.Errorf("evaluation_setup_error: runtime-real profile mismatch")
		}
	case ScenarioCodingFake:
		if p.EvaluationPlane != PlaneCoding || p.ModelProvider != nil || p.ModelName != nil || value(p.Executor) != ExecutorLocalFake || value(p.Verifier) != VerifierDeterministic || p.NetworkMode != NetworkDisabled {
			return fmt.Errorf("evaluation_setup_error: coding-fake profile mismatch")
		}
	case ScenarioCodingReal:
		if p.EvaluationPlane != PlaneCoding || value(p.Executor) != ExecutorIsolated || value(p.Verifier) != VerifierHiddenIsolated || p.NetworkMode == NetworkDisabled {
			return fmt.Errorf("evaluation_setup_error: coding-real profile mismatch")
		}
	default:
		return fmt.Errorf("evaluation_setup_error: invalid scenario %q", p.Scenario)
	}
	if p.Tier != TierPR && p.Tier != TierNightly && p.Tier != TierRelease {
		return fmt.Errorf("evaluation_setup_error: invalid tier %q", p.Tier)
	}
	if p.NetworkMode != NetworkDisabled && p.NetworkMode != NetworkAllowlist && p.NetworkMode != NetworkEnabled {
		return fmt.Errorf("evaluation_setup_error: invalid network_mode %q", p.NetworkMode)
	}
	return nil
}

func value(v *string) string {
	if v == nil {
		return ""
	}
	return *v
}
