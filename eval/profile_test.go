package eval

import "testing"

func TestEvaluationProfileValidation(t *testing.T) {
	tests := []struct {
		name    string
		profile EvaluationProfile
		wantErr bool
	}{
		{name: "runtime fake", profile: RuntimeFakeProfile(TierPR)},
		{name: "coding fake", profile: EvaluationProfile{EvaluationPlane: PlaneCoding, Scenario: ScenarioCodingFake, Executor: String(ExecutorLocalFake), Verifier: String(VerifierDeterministic), NetworkMode: NetworkDisabled, Tier: TierPR}},
		{name: "runtime real", profile: EvaluationProfile{EvaluationPlane: PlaneRuntime, Scenario: ScenarioRuntimeReal, ModelProvider: String(ProviderDeepSeek), ModelName: String("deepseek-chat"), Executor: String(ExecutorLocalFake), Verifier: String(VerifierDeterministic), NetworkMode: NetworkEnabled, Tier: TierNightly}},
		{name: "coding real", profile: EvaluationProfile{EvaluationPlane: PlaneCoding, Scenario: ScenarioCodingReal, ModelProvider: String(ProviderDeepSeek), ModelName: String("agent"), Executor: String(ExecutorIsolated), Verifier: String(VerifierHiddenIsolated), NetworkMode: NetworkAllowlist, Tier: TierRelease}},
		{name: "runtime fake cannot be real provider", profile: EvaluationProfile{EvaluationPlane: PlaneRuntime, Scenario: ScenarioRuntimeFake, ModelProvider: String(ProviderDeepSeek), ModelName: String("deepseek-chat"), Executor: String(ExecutorLocalFake), Verifier: String(VerifierDeterministic), NetworkMode: NetworkDisabled, Tier: TierPR}, wantErr: true},
		{name: "coding fake has no provider", profile: EvaluationProfile{EvaluationPlane: PlaneCoding, Scenario: ScenarioCodingFake, ModelProvider: String(ProviderFake), Executor: String(ExecutorLocalFake), Verifier: String(VerifierDeterministic), NetworkMode: NetworkDisabled, Tier: TierPR}, wantErr: true},
		{name: "invalid tier", profile: RuntimeFakeProfile("adhoc"), wantErr: true},
		{name: "invalid evaluation plane", profile: EvaluationProfile{EvaluationPlane: "sandbox", Scenario: ScenarioRuntimeFake, ModelProvider: String(ProviderFake), Executor: String(ExecutorLocalFake), Verifier: String(VerifierDeterministic), NetworkMode: NetworkDisabled, Tier: TierPR}, wantErr: true},
		{name: "invalid scenario", profile: EvaluationProfile{EvaluationPlane: PlaneRuntime, Scenario: "runtime-simulated", ModelProvider: String(ProviderFake), Executor: String(ExecutorLocalFake), Verifier: String(VerifierDeterministic), NetworkMode: NetworkDisabled, Tier: TierPR}, wantErr: true},
		{name: "empty scenario", profile: EvaluationProfile{EvaluationPlane: PlaneRuntime, NetworkMode: NetworkDisabled, Tier: TierPR}, wantErr: true},
		{name: "invalid network mode", profile: EvaluationProfile{EvaluationPlane: PlaneRuntime, Scenario: ScenarioRuntimeFake, ModelProvider: String(ProviderFake), ModelName: String("fake-supervisor@1"), Executor: String(ExecutorLocalFake), Verifier: String(VerifierDeterministic), NetworkMode: "offline-ish", Tier: TierPR}, wantErr: true},
		{name: "runtime fake requires disabled network", profile: EvaluationProfile{EvaluationPlane: PlaneRuntime, Scenario: ScenarioRuntimeFake, ModelProvider: String(ProviderFake), ModelName: String("fake-supervisor@1"), Executor: String(ExecutorLocalFake), Verifier: String(VerifierDeterministic), NetworkMode: NetworkEnabled, Tier: TierPR}, wantErr: true},
		{name: "runtime fake requires disabled network allowlist", profile: EvaluationProfile{EvaluationPlane: PlaneRuntime, Scenario: ScenarioRuntimeFake, ModelProvider: String(ProviderFake), ModelName: String("fake-supervisor@1"), Executor: String(ExecutorLocalFake), Verifier: String(VerifierDeterministic), NetworkMode: NetworkAllowlist, Tier: TierPR}, wantErr: true},
		{name: "runtime fake requires deterministic verifier", profile: EvaluationProfile{EvaluationPlane: PlaneRuntime, Scenario: ScenarioRuntimeFake, ModelProvider: String(ProviderFake), ModelName: String("fake-supervisor@1"), Executor: String(ExecutorLocalFake), Verifier: String(VerifierHiddenIsolated), NetworkMode: NetworkDisabled, Tier: TierPR}, wantErr: true},
		{name: "coding real requires isolated executor", profile: EvaluationProfile{EvaluationPlane: PlaneCoding, Scenario: ScenarioCodingReal, Executor: String(ExecutorLocalFake), Verifier: String(VerifierHiddenIsolated), NetworkMode: NetworkAllowlist, Tier: TierRelease}, wantErr: true},
		{name: "coding real requires network", profile: EvaluationProfile{EvaluationPlane: PlaneCoding, Scenario: ScenarioCodingReal, Executor: String(ExecutorIsolated), Verifier: String(VerifierHiddenIsolated), NetworkMode: NetworkDisabled, Tier: TierRelease}, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := tt.profile.Validate(); (err != nil) != tt.wantErr {
				t.Fatalf("Validate() error=%v wantErr=%v", err, tt.wantErr)
			}
		})
	}
}
