package ngenrt

import (
	"context"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"ngen/internal/artifact"
	"ngen/internal/provider"
	"ngen/internal/task"
)

func (s *Service) CreateMission(ctx context.Context, req task.MissionCreateRequest) (task.MissionView, error) {
	if err := s.Store.EnsureWorkspaceLayout(); err != nil {
		return task.MissionView{}, err
	}
	req.Title = strings.TrimSpace(req.Title)
	req.Objective = strings.TrimSpace(req.Objective)
	req.RootTaskID = strings.TrimSpace(req.RootTaskID)
	req.Criteria = normalizeMissionStrings(req.Criteria)
	req.Constraints = normalizeMissionStrings(req.Constraints)
	if req.Objective == "" {
		return task.MissionView{}, fmt.Errorf("mission objective is required")
	}
	if req.Title == "" {
		req.Title = missionTitleFromObjective(req.Objective)
	}
	if len(req.Criteria) == 0 {
		req.Criteria = []string{"mission objective is satisfied with evidence"}
	}
	evidenceRequirements := []string{
		"Root task reaches Done through the existing verifier/review/completion gate.",
		"Open criteria count is zero in criteria/latest.json.",
		"Latest completion report is accepted.",
	}
	assertions := missionContractAssertions(req.Criteria, evidenceRequirements)
	assertionIDs := missionAssertionIDs(assertions)

	var root task.Spec
	var err error
	if req.RootTaskID == "" {
		root, err = s.Create(ctx, task.TaskFile{
			Kind:            task.KindCoding,
			Title:           req.Title,
			Objective:       req.Objective,
			Constraints:     append([]string{"Mission root task: satisfy validation_contract.json before Done."}, req.Constraints...),
			SuccessCriteria: criteriaFromStrings(req.Criteria),
		})
		if err != nil {
			return task.MissionView{}, err
		}
	} else {
		root, err = s.Store.LoadTask(req.RootTaskID)
		if err != nil {
			return task.MissionView{}, err
		}
	}

	missionID := task.NewID("MIS")
	now := task.Now()
	if err := s.Store.EnsureMissionLayout(missionID); err != nil {
		return task.MissionView{}, err
	}
	contract := task.MissionValidationContract{
		ObjectKind:             "mission_validation_contract",
		SchemaVersion:          task.SchemaVersion,
		MissionID:              missionID,
		ContractID:             task.NewID("MCON"),
		BehavioralRequirements: append([]string(nil), req.Criteria...),
		AcceptanceTests:        append([]string(nil), req.Criteria...),
		Assertions:             assertions,
		NegativeCases:          missionNegativeCases(req.Criteria),
		NonGoals:               []string{"Do not replace task, project, worker, review, or verifier truth with mission prose."},
		ManualChecks:           missionManualChecks(req.Criteria),
		EvidenceRequirements:   evidenceRequirements,
		CreatedFromTaskRef:     fmt.Sprintf("workspace:.ngen/tasks/%s/task.json", root.TaskID),
		CreatedAt:              now,
		UpdatedAt:              now,
	}
	features := task.MissionFeatureSet{
		ObjectKind:    "mission_features",
		SchemaVersion: task.SchemaVersion,
		MissionID:     missionID,
		Features: []task.MissionFeature{
			{
				FeatureID:        "FEAT-001",
				Title:            req.Title,
				Description:      req.Objective,
				BoundTaskID:      root.TaskID,
				ContractCoverage: append([]string(nil), assertionIDs...),
				Status:           task.ProjectStepStatusPending,
				EvidenceRefs:     []string{fmt.Sprintf("workspace:.ngen/tasks/%s/task.json", root.TaskID)},
				UpdatedAt:        now,
			},
		},
		UpdatedAt: now,
	}
	milestones := task.MissionMilestoneSet{
		ObjectKind:       "mission_milestones",
		SchemaVersion:    task.SchemaVersion,
		MissionID:        missionID,
		CurrentFeatureID: "FEAT-001",
		ReadyFeatureIDs:  []string{"FEAT-001"},
		Milestones: []task.MissionMilestone{
			{
				MilestoneID:      "MS-001",
				Title:            req.Title,
				FeatureIDs:       []string{"FEAT-001"},
				BoundTaskIDs:     []string{root.TaskID},
				ContractCoverage: append([]string(nil), assertionIDs...),
				Status:           task.ProjectStepStatusPending,
				EvidenceRefs:     []string{fmt.Sprintf("workspace:.ngen/tasks/%s/task.json", root.TaskID)},
				UpdatedAt:        now,
			},
		},
		UpdatedAt: now,
	}
	mission := task.Mission{
		ObjectKind:            "mission",
		SchemaVersion:         task.SchemaVersion,
		MissionID:             missionID,
		Title:                 req.Title,
		Objective:             req.Objective,
		RootTaskID:            root.TaskID,
		ProjectRef:            "workspace:.ngen/project/project.json",
		ValidationContractRef: "validation_contract.json",
		CurrentMilestoneID:    "MS-001",
		Status:                task.MissionStatusDraft,
		StatusReasonCode:      "awaiting_plan_approval",
		PlanApprovalStatus:    task.MissionPlanApprovalPending,
		FeatureRefs:           []string{"features.json#feature_id=FEAT-001"},
		MilestoneRefs:         []string{"milestones.json#milestone_id=MS-001"},
		CreatedAt:             now,
		UpdatedAt:             now,
	}
	rolePlan, err := s.missionRolePlan()
	if err != nil {
		return task.MissionView{}, err
	}
	mission.RolePlan = rolePlan
	if err := s.Store.SaveMission(mission); err != nil {
		return task.MissionView{}, err
	}
	if err := s.Store.SaveMissionValidationContract(contract); err != nil {
		return task.MissionView{}, err
	}
	if err := s.Store.SaveMissionFeatures(features); err != nil {
		return task.MissionView{}, err
	}
	if err := s.Store.SaveMissionMilestones(milestones); err != nil {
		return task.MissionView{}, err
	}
	if err := s.saveMissionNotes(mission, contract); err != nil {
		return task.MissionView{}, err
	}
	if err := s.appendMissionTaskEvent(root.TaskID, "mission_created", fmt.Sprintf("Created mission %s.", missionID), []string{missionWorkspaceRef(missionID, "mission.json"), missionWorkspaceRef(missionID, "validation_contract.json")}); err != nil {
		return task.MissionView{}, err
	}
	if err := s.syncMissionRootNarrative(root.TaskID, fmt.Sprintf("Created mission %s.", missionID)); err != nil {
		return task.MissionView{}, err
	}
	return s.GetMission(ctx, missionID)
}

func (s *Service) GetMission(ctx context.Context, missionID string) (task.MissionView, error) {
	_ = ctx
	return s.missionView(missionID)
}

func (s *Service) MissionStatus(ctx context.Context, missionID string) (task.MissionView, error) {
	return s.GetMission(ctx, missionID)
}

func (s *Service) MissionPlan(ctx context.Context, missionID string) (task.MissionPlanView, error) {
	mission, err := s.Store.LoadMission(missionID)
	if err != nil {
		return task.MissionPlanView{}, err
	}
	features, err := s.Store.LoadMissionFeatures(missionID)
	if err != nil {
		return task.MissionPlanView{}, err
	}
	milestones, err := s.Store.LoadMissionMilestones(missionID)
	if err != nil {
		return task.MissionPlanView{}, err
	}
	features, milestones = s.refreshMissionFeatureRuntimeState(ctx, mission, features, milestones)
	if err := s.Store.SaveMissionFeatures(features); err != nil {
		return task.MissionPlanView{}, err
	}
	if err := s.Store.SaveMissionMilestones(milestones); err != nil {
		return task.MissionPlanView{}, err
	}
	return task.MissionPlanView{
		ObjectKind:    "mission_plan",
		SchemaVersion: task.SchemaVersion,
		MissionID:     strings.TrimSpace(missionID),
		Features:      features,
		Milestones:    milestones,
	}, nil
}

func (s *Service) ApproveMissionPlan(ctx context.Context, missionID string) (task.MissionView, error) {
	mission, err := s.Store.LoadMission(missionID)
	if err != nil {
		return task.MissionView{}, err
	}
	contract, err := s.Store.LoadMissionValidationContract(missionID)
	if err != nil {
		return task.MissionView{}, err
	}
	features, err := s.Store.LoadMissionFeatures(missionID)
	if err != nil {
		return task.MissionView{}, err
	}
	milestones, err := s.Store.LoadMissionMilestones(missionID)
	if err != nil {
		return task.MissionView{}, err
	}
	if findings := missionContractCoverageFindings(contract, features, milestones); len(findings) > 0 {
		run := task.MissionValidationRun{
			ObjectKind:            "mission_validation_run",
			SchemaVersion:         task.SchemaVersion,
			ValidationRunID:       task.NewID("MVAL"),
			MissionID:             mission.MissionID,
			MilestoneID:           mission.CurrentMilestoneID,
			RootTaskID:            mission.RootTaskID,
			ValidatorRole:         task.MissionRoleOrchestrator,
			ValidatorKind:         "deterministic_plan_approval",
			Status:                "blocking",
			Summary:               fmt.Sprintf("Mission plan approval blocked by %d contract coverage finding(s).", len(findings)),
			ContractCoverageCount: len(effectiveMissionAssertions(contract)),
			Findings:              findings,
			EvidenceRefs:          []string{"mission.json", "validation_contract.json", "features.json", "milestones.json"},
			ValidatorContextRefs:  []string{"mission.json", "validation_contract.json", "features.json", "milestones.json"},
			CreatedAt:             task.Now(),
		}
		return s.persistMissionPreflightBlock(ctx, mission, run, "blocked_contract_coverage")
	}
	now := task.Now()
	mission.PlanApprovalStatus = task.MissionPlanApprovalApproved
	mission.PlanApprovedAt = now
	mission.PlanApprovedBy = "operator"
	mission.PlanApprovedContractRef = missionApprovedContractRef(contract)
	mission.LatestValidationRef = ""
	if mission.Status != task.MissionStatusDone && mission.Status != task.MissionStatusPaused {
		mission.Status = task.MissionStatusDraft
		mission.StatusReasonCode = ""
	}
	mission.UpdatedAt = now
	if err := s.Store.SaveMission(mission); err != nil {
		return task.MissionView{}, err
	}
	if err := s.appendMissionTaskEvent(mission.RootTaskID, "mission_plan_approved", fmt.Sprintf("Approved mission %s validation contract.", mission.MissionID), []string{missionWorkspaceRef(mission.MissionID, "mission.json"), missionWorkspaceRef(mission.MissionID, mission.PlanApprovedContractRef)}); err != nil {
		return task.MissionView{}, err
	}
	if err := s.syncMissionRootNarrative(mission.RootTaskID, fmt.Sprintf("Approved mission %s validation contract.", mission.MissionID)); err != nil {
		return task.MissionView{}, err
	}
	return s.GetMission(ctx, mission.MissionID)
}

func (s *Service) missionRolePlan() (map[string]task.MissionRolePlanEntry, error) {
	plan := make(map[string]task.MissionRolePlanEntry, len(task.SupportedMissionRoles()))
	for _, role := range task.SupportedMissionRoles() {
		_, resolution, err := task.ProviderConfigForMissionRole(s.Config, role)
		if err != nil {
			return nil, err
		}
		plan[role] = task.MissionRolePlanEntry{
			Model:    resolution.Model,
			Source:   resolution.Source,
			Explicit: resolution.Explicit,
		}
	}
	return plan, nil
}

func (s *Service) serviceWithProviderConfig(cfg task.ProviderConfig) *Service {
	scoped := *s
	scoped.Config = s.Config
	scoped.Config.Provider = cfg
	return &scoped
}

func (s *Service) serviceForMissionRole(mission task.Mission, role string) (*Service, task.MissionRoleModelResolution, error) {
	canonicalRole, err := task.NormalizeMissionRole(role)
	if err != nil {
		return nil, task.MissionRoleModelResolution{}, err
	}
	cfg, resolution, err := task.ProviderConfigForMissionRole(s.Config, canonicalRole)
	if err != nil {
		return nil, task.MissionRoleModelResolution{}, err
	}
	if entry, ok := mission.RolePlan[canonicalRole]; ok {
		cfg.Model = strings.TrimSpace(entry.Model)
		resolution = task.MissionRoleModelResolution{
			Role:     canonicalRole,
			Model:    strings.TrimSpace(entry.Model),
			Source:   firstNonEmpty(strings.TrimSpace(entry.Source), task.MissionRoleModelSourceEmpty),
			Explicit: entry.Explicit,
		}
	}
	return s.serviceWithProviderConfig(cfg), resolution, nil
}

func (s *Service) ValidateMission(ctx context.Context, missionID, milestoneID string) (view task.MissionView, err error) {
	started := time.Now()
	metricsMissionID := ""
	defer func() {
		if metricsMissionID == "" {
			return
		}
		status := "error"
		if view.Mission.Status != "" {
			status = view.Mission.Status
		}
		validatorTimeMS := time.Since(started).Milliseconds()
		metricErr := s.appendMissionMetricsRecord(ctx, metricsMissionID, "mission_validate", status, time.Since(started), []string{"mission.json", "validation_contract.json", "features.json", "milestones.json", "validation_runs.jsonl"}, &validatorTimeMS)
		if metricErr == nil && view.Mission.RootTaskID != "" {
			metricErr = s.syncMissionRootNarrative(view.Mission.RootTaskID, fmt.Sprintf("Recorded mission %s validation metrics.", metricsMissionID))
		}
		if metricErr == nil {
			return
		}
		if err == nil {
			err = metricErr
			return
		}
		err = fmt.Errorf("%v; record mission validation metrics: %w", err, metricErr)
	}()
	mission, err := s.Store.LoadMission(missionID)
	if err != nil {
		return task.MissionView{}, err
	}
	metricsMissionID = mission.MissionID
	if strings.TrimSpace(milestoneID) == "" {
		milestoneID = mission.CurrentMilestoneID
	}
	contract, err := s.Store.LoadMissionValidationContract(missionID)
	if err != nil {
		return task.MissionView{}, err
	}
	features, err := s.Store.LoadMissionFeatures(missionID)
	if err != nil {
		return task.MissionView{}, err
	}
	milestones, err := s.Store.LoadMissionMilestones(missionID)
	if err != nil {
		return task.MissionView{}, err
	}
	features, milestones = s.refreshMissionFeatureRuntimeState(ctx, mission, features, milestones)
	if err := s.Store.SaveMissionFeatures(features); err != nil {
		return task.MissionView{}, err
	}
	if err := s.Store.SaveMissionMilestones(milestones); err != nil {
		return task.MissionView{}, err
	}

	run := task.MissionValidationRun{
		ObjectKind:            "mission_validation_run",
		SchemaVersion:         task.SchemaVersion,
		ValidationRunID:       task.NewID("MVAL"),
		MissionID:             mission.MissionID,
		MilestoneID:           milestoneID,
		RootTaskID:            mission.RootTaskID,
		ValidatorRole:         task.MissionRoleValidators,
		ValidatorKind:         "deterministic_artifact",
		ContractCoverageCount: len(effectiveMissionAssertions(contract)),
		CreatedAt:             task.Now(),
		EvidenceRefs: []string{
			"mission.json",
			"validation_contract.json",
			"features.json",
			"milestones.json",
			fmt.Sprintf("workspace:.ngen/tasks/%s/task.json", mission.RootTaskID),
			fmt.Sprintf("workspace:.ngen/tasks/%s/state.json", mission.RootTaskID),
		},
	}
	if entry, ok := mission.RolePlan[task.MissionRoleValidators]; ok {
		run.ValidatorModel = strings.TrimSpace(entry.Model)
		run.ValidatorModelSource = strings.TrimSpace(entry.Source)
		run.ValidatorModelExplicit = entry.Explicit
	}
	run.ValidatorContextRefs = append([]string(nil), run.EvidenceRefs...)
	run.Findings = append(run.Findings, missionPlanGateFindings(mission, contract, features, milestones, true)...)
	rootStatus, statusErr := s.Status(ctx, mission.RootTaskID)
	if statusErr != nil {
		run.Findings = append(run.Findings, missionFinding("missing_root_status", "critical", true, fmt.Sprintf("Root task status is unavailable: %v.", statusErr), []string{fmt.Sprintf("workspace:.ngen/tasks/%s/task.json", mission.RootTaskID)}, "Restore or recreate the root task before validating the mission."))
	} else {
		run.EvidenceRefs = append(run.EvidenceRefs, fmt.Sprintf("workspace:.ngen/tasks/%s/state.json", mission.RootTaskID))
		run.ValidatorContextRefs = append(run.ValidatorContextRefs, fmt.Sprintf("workspace:.ngen/tasks/%s/state.json", mission.RootTaskID))
		if rootStatus.State != task.StateDone {
			run.Findings = append(run.Findings, missionFinding("missing_evidence", "high", true, fmt.Sprintf("Root task is %s/%s, not Done.", rootStatus.Phase, rootStatus.State), []string{fmt.Sprintf("workspace:.ngen/tasks/%s/state.json", mission.RootTaskID)}, "Run or resume the root task before closing the mission milestone."))
		}
	}
	var criteria *task.CriteriaSnapshot
	if loadedCriteria, criteriaErr := s.Store.LoadCriteria(mission.RootTaskID); criteriaErr != nil {
		run.Findings = append(run.Findings, missionFinding("missing_criteria", "high", true, fmt.Sprintf("Root task criteria are unavailable: %v.", criteriaErr), []string{fmt.Sprintf("workspace:.ngen/tasks/%s/criteria/latest.json", mission.RootTaskID)}, "Rebuild task criteria evidence before validating the mission."))
	} else {
		criteriaCopy := loadedCriteria
		criteria = &criteriaCopy
		run.EvidenceRefs = append(run.EvidenceRefs, fmt.Sprintf("workspace:.ngen/tasks/%s/criteria/latest.json", mission.RootTaskID))
		run.ValidatorContextRefs = append(run.ValidatorContextRefs, fmt.Sprintf("workspace:.ngen/tasks/%s/criteria/latest.json", mission.RootTaskID))
		if loadedCriteria.OpenCount > 0 {
			run.Findings = append(run.Findings, missionFinding("open_criteria", "high", true, fmt.Sprintf("Root task still has %d open criteria.", loadedCriteria.OpenCount), []string{fmt.Sprintf("workspace:.ngen/tasks/%s/criteria/latest.json", mission.RootTaskID)}, "Close open criteria through task execution or explicit waiver before mission completion."))
		}
	}
	var completion *task.CompletionReport
	if loadedCompletion, completionErr := s.Store.LoadCompletion(mission.RootTaskID); completionErr != nil {
		run.Findings = append(run.Findings, missionFinding("missing_completion", "high", true, fmt.Sprintf("Root task completion report is unavailable: %v.", completionErr), []string{fmt.Sprintf("workspace:.ngen/tasks/%s/completion/latest.json", mission.RootTaskID)}, "Run review/completion gate before mission validation."))
	} else {
		completionCopy := loadedCompletion
		completion = &completionCopy
		run.EvidenceRefs = append(run.EvidenceRefs, fmt.Sprintf("workspace:.ngen/tasks/%s/completion/latest.json", mission.RootTaskID))
		run.ValidatorContextRefs = append(run.ValidatorContextRefs, fmt.Sprintf("workspace:.ngen/tasks/%s/completion/latest.json", mission.RootTaskID))
		if loadedCompletion.Status != "accepted" {
			run.Findings = append(run.Findings, missionFinding("completion_not_accepted", "high", true, fmt.Sprintf("Root task completion status is %s.", loadedCompletion.Status), []string{fmt.Sprintf("workspace:.ngen/tasks/%s/completion/latest.json", mission.RootTaskID)}, "Satisfy review/completion gate before mission completion."))
		}
	}
	var harness *task.HarnessEvaluation
	if loadedHarness, harnessErr := s.Store.LoadHarnessEvaluation(mission.RootTaskID); harnessErr == nil && loadedHarness.HarnessEvalID != "" {
		harness = &loadedHarness
		run.EvidenceRefs = append(run.EvidenceRefs, fmt.Sprintf("workspace:.ngen/tasks/%s/harness/latest.json", mission.RootTaskID))
		run.ValidatorContextRefs = append(run.ValidatorContextRefs, fmt.Sprintf("workspace:.ngen/tasks/%s/harness/latest.json", mission.RootTaskID))
	}
	run.Findings = append(run.Findings, missionAssertionEvidenceFindings(contract, features, milestones)...)

	run.EvidenceRefs = uniqueRefs(run.EvidenceRefs)
	run.ValidatorContextRefs = uniqueRefs(run.ValidatorContextRefs)
	run.Findings = append(run.Findings, missionUserTestingSkippedFinding())
	if hasBlockingMissionFindings(run.Findings) {
		run.Status = "blocking"
		run.Summary = fmt.Sprintf("Mission validation blocked by %d finding(s).", missionBlockingFindingCount(run.Findings))
		view, err = s.persistMissionValidationRun(ctx, mission, features, milestones, run, milestoneID, effectiveMissionAssertions(contract))
		return view, err
	}
	if missionModelValidatorEnabled(mission) {
		modelRun, err := s.modelMissionValidationRun(ctx, mission, contract, features, milestones, milestoneID, rootStatus, criteria, completion, harness, run.EvidenceRefs)
		if err != nil {
			return task.MissionView{}, err
		}
		modelRun.Findings = append(modelRun.Findings, missionUserTestingSkippedFinding())
		view, err = s.persistMissionValidationRun(ctx, mission, features, milestones, modelRun, milestoneID, effectiveMissionAssertions(contract))
		return view, err
	}
	run.Status = "passed"
	run.Summary = "Mission validation passed: root task Done, criteria closed, and completion accepted."
	view, err = s.persistMissionValidationRun(ctx, mission, features, milestones, run, milestoneID, effectiveMissionAssertions(contract))
	return view, err
}

func (s *Service) persistMissionValidationRun(ctx context.Context, mission task.Mission, features task.MissionFeatureSet, milestones task.MissionMilestoneSet, run task.MissionValidationRun, milestoneID string, assertions []task.MissionContractAssertion) (task.MissionView, error) {
	validationRef := artifact.MissionValidationRunRef(run.ValidationRunID)
	if len(run.Findings) == 0 && run.Status == "" {
		run.Status = "passed"
		run.Summary = "Mission validation passed: root task Done, criteria closed, and completion accepted."
	}
	run.EvidenceRefs = uniqueRefs(run.EvidenceRefs)
	run.ValidatorContextRefs = uniqueRefs(run.ValidatorContextRefs)
	blocking := run.Status == "blocking" || hasBlockingMissionFindings(run.Findings)
	if !blocking {
		run.Status = "passed"
		if strings.TrimSpace(run.Summary) == "" {
			run.Summary = "Mission validation passed: root task Done, criteria closed, and completion accepted."
		}
		mission.Status = task.MissionStatusDone
		mission.StatusReasonCode = ""
		features, milestones = markMissionSets(features, milestones, milestoneID, task.ProjectStepStatusCompleted, validationRef, run.EvidenceRefs)
	} else {
		run.Status = "blocking"
		if strings.TrimSpace(run.Summary) == "" {
			run.Summary = fmt.Sprintf("Mission validation blocked by %d finding(s).", missionBlockingFindingCount(run.Findings))
		}
		mission.Status = task.MissionStatusBlocked
		mission.StatusReasonCode = "blocked_validation"
		features, milestones = markMissionSets(features, milestones, milestoneID, task.ProjectStepStatusBlocked, validationRef, run.EvidenceRefs)
		var addedFixFeatures []string
		features, milestones, addedFixFeatures = appendMissionFixFeatures(features, milestones, run, validationRef, milestoneID, assertions, mission.RootTaskID)
		features, milestones = refreshMissionFeatureSchedule(features, milestones)
		if len(addedFixFeatures) > 0 {
			if err := s.scopeMissionFixFeatures(ctx, mission, features, addedFixFeatures, validationRef); err != nil {
				return task.MissionView{}, err
			}
		}
	}
	if err := s.Store.AppendMissionValidationRun(run); err != nil {
		return task.MissionView{}, err
	}
	mission.LatestValidationRef = validationRef
	mission.UpdatedAt = task.Now()
	if err := s.Store.SaveMission(mission); err != nil {
		return task.MissionView{}, err
	}
	if err := s.Store.SaveMissionFeatures(features); err != nil {
		return task.MissionView{}, err
	}
	if err := s.Store.SaveMissionMilestones(milestones); err != nil {
		return task.MissionView{}, err
	}
	if err := s.appendMissionTaskEvent(mission.RootTaskID, "mission_validated", run.Summary, []string{missionWorkspaceRef(mission.MissionID, validationRef)}); err != nil {
		return task.MissionView{}, err
	}
	if err := s.syncMissionRootNarrative(mission.RootTaskID, run.Summary); err != nil {
		return task.MissionView{}, err
	}
	return s.GetMission(ctx, mission.MissionID)
}

func (s *Service) persistMissionPreflightBlock(ctx context.Context, mission task.Mission, run task.MissionValidationRun, statusReason string) (task.MissionView, error) {
	validationRef := artifact.MissionValidationRunRef(run.ValidationRunID)
	run.Status = "blocking"
	if strings.TrimSpace(run.Summary) == "" {
		run.Summary = fmt.Sprintf("Mission blocked by %d plan gate finding(s).", len(run.Findings))
	}
	run.EvidenceRefs = uniqueRefs(run.EvidenceRefs)
	run.ValidatorContextRefs = uniqueRefs(run.ValidatorContextRefs)
	if err := s.Store.AppendMissionValidationRun(run); err != nil {
		return task.MissionView{}, err
	}
	mission.Status = task.MissionStatusBlocked
	mission.StatusReasonCode = firstNonEmpty(statusReason, "blocked_plan_gate")
	mission.LatestValidationRef = validationRef
	mission.UpdatedAt = task.Now()
	if err := s.Store.SaveMission(mission); err != nil {
		return task.MissionView{}, err
	}
	if err := s.appendMissionTaskEvent(mission.RootTaskID, "mission_plan_blocked", run.Summary, []string{missionWorkspaceRef(mission.MissionID, validationRef)}); err != nil {
		return task.MissionView{}, err
	}
	if err := s.syncMissionRootNarrative(mission.RootTaskID, run.Summary); err != nil {
		return task.MissionView{}, err
	}
	return s.GetMission(ctx, mission.MissionID)
}

func missionModelValidatorEnabled(mission task.Mission) bool {
	entry, ok := mission.RolePlan[task.MissionRoleValidators]
	return ok && entry.Explicit && strings.TrimSpace(entry.Model) != ""
}

func (s *Service) modelMissionValidationRun(
	ctx context.Context,
	mission task.Mission,
	contract task.MissionValidationContract,
	features task.MissionFeatureSet,
	milestones task.MissionMilestoneSet,
	milestoneID string,
	rootStatus task.StatusSnapshot,
	criteria *task.CriteriaSnapshot,
	completion *task.CompletionReport,
	harness *task.HarnessEvaluation,
	deterministicRefs []string,
) (task.MissionValidationRun, error) {
	scoped, resolution, err := s.serviceForMissionRole(mission, task.MissionRoleValidators)
	if err != nil {
		return task.MissionValidationRun{}, err
	}
	contextRefs := uniqueRefs(append([]string{
		"mission.json",
		"validation_contract.json",
		"features.json",
		"milestones.json",
		fmt.Sprintf("workspace:.ngen/tasks/%s/state.json", mission.RootTaskID),
		fmt.Sprintf("workspace:.ngen/tasks/%s/criteria/latest.json", mission.RootTaskID),
		fmt.Sprintf("workspace:.ngen/tasks/%s/completion/latest.json", mission.RootTaskID),
	}, deterministicRefs...))
	if harness != nil && harness.HarnessEvalID != "" {
		contextRefs = uniqueRefs(append(contextRefs, fmt.Sprintf("workspace:.ngen/tasks/%s/harness/latest.json", mission.RootTaskID)))
	}
	result, err := provider.GenerateMissionValidation(ctx, scoped.Config.Provider, provider.MissionValidationInput{
		Mission:     mission,
		Contract:    contract,
		Features:    features,
		Milestones:  milestones,
		RootStatus:  &rootStatus,
		Criteria:    criteria,
		Completion:  completion,
		Harness:     harness,
		ContextRefs: contextRefs,
	})
	if err != nil {
		return task.MissionValidationRun{}, err
	}
	tokenUsage, promptCacheUsage := providerUsageFromMissionValidation(result)
	usageRecord, usageErr := s.appendProviderUsage(mission.RootTaskID, providerUsageOperationMissionValidation, scoped.Config.Provider, tokenUsage, promptCacheUsage, []string{
		missionWorkspaceRef(mission.MissionID, "mission.json"),
		missionWorkspaceRef(mission.MissionID, "validation_contract.json"),
		missionWorkspaceRef(mission.MissionID, "features.json"),
		missionWorkspaceRef(mission.MissionID, "milestones.json"),
	})
	if usageErr != nil {
		return task.MissionValidationRun{}, usageErr
	}
	run := task.MissionValidationRun{
		ObjectKind:             "mission_validation_run",
		SchemaVersion:          task.SchemaVersion,
		ValidationRunID:        task.NewID("MVAL"),
		MissionID:              mission.MissionID,
		MilestoneID:            milestoneID,
		RootTaskID:             mission.RootTaskID,
		ValidatorRole:          task.MissionRoleValidators,
		ValidatorKind:          "model_validator",
		ValidatorModel:         resolution.Model,
		ValidatorModelSource:   resolution.Source,
		ValidatorModelExplicit: resolution.Explicit,
		ValidatorContextRefs:   contextRefs,
		ProviderUsageRef:       providerUsageRef(usageRecord),
		ContractCoverageCount:  len(effectiveMissionAssertions(contract)),
		Status:                 result.Status,
		Summary:                result.Summary,
		Findings:               result.Findings,
		EvidenceRefs:           uniqueRefs(append(contextRefs, providerUsageRef(usageRecord))),
		TokenUsage:             tokenUsage,
		PromptCacheUsage:       promptCacheUsage,
		CreatedAt:              task.Now(),
	}
	if run.Status == "" {
		run.Status = "passed"
		for _, finding := range run.Findings {
			if finding.Blocking {
				run.Status = "blocking"
				break
			}
		}
	}
	if strings.TrimSpace(run.Summary) == "" {
		if run.Status == "passed" {
			run.Summary = "Model validator passed with no blocking findings."
		} else {
			run.Summary = fmt.Sprintf("Model validator blocked mission with %d finding(s).", len(run.Findings))
		}
	}
	return run, nil
}

func appendMissionFixFeatures(features task.MissionFeatureSet, milestones task.MissionMilestoneSet, run task.MissionValidationRun, validationRef, milestoneID string, assertions []task.MissionContractAssertion, rootTaskID string) (task.MissionFeatureSet, task.MissionMilestoneSet, []string) {
	now := task.Now()
	existing := make(map[string]struct{}, len(features.Features))
	existingFindingRefs := make(map[string]struct{}, len(features.Features))
	for _, feature := range features.Features {
		existing[feature.FeatureID] = struct{}{}
		for _, findingRef := range feature.ValidatorFindingRefs {
			existingFindingRefs[strings.TrimSpace(findingRef)] = struct{}{}
		}
	}
	var added []string
	for idx, finding := range run.Findings {
		if !finding.Blocking {
			continue
		}
		if !missionFindingCreatesFixFeature(finding) {
			continue
		}
		findingRef := fmt.Sprintf("%s#finding_id=%s", validationRef, finding.FindingID)
		if _, ok := existingFindingRefs[findingRef]; ok {
			continue
		}
		featureID := fmt.Sprintf("FIX-%03d", len(existing)+idx+1)
		if _, ok := existing[featureID]; ok {
			featureID = fmt.Sprintf("FIX-%s", strings.TrimPrefix(finding.FindingID, "MFIND-"))
		}
		existing[featureID] = struct{}{}
		title := firstNonEmpty(strings.TrimSpace(finding.RecommendedAction), strings.TrimSpace(finding.Summary), "Resolve validator finding")
		features.Features = append(features.Features, task.MissionFeature{
			FeatureID:            featureID,
			Title:                missionTitleFromObjective(title),
			Description:          strings.TrimSpace(finding.Summary),
			DependsOn:            []string{"FEAT-001"},
			BoundTaskID:          strings.TrimSpace(rootTaskID),
			ContractCoverage:     missionFindingAssertionCoverage(finding, assertions),
			Status:               task.ProjectStepStatusPending,
			EvidenceRefs:         uniqueRefs(append([]string{validationRef}, finding.EvidenceRefs...)),
			ValidatorFindingRefs: []string{findingRef},
			UpdatedAt:            now,
		})
		added = append(added, featureID)
	}
	if len(added) == 0 {
		return features, milestones, nil
	}
	features.UpdatedAt = now
	for idx := range milestones.Milestones {
		if milestoneID != "" && milestones.Milestones[idx].MilestoneID != milestoneID {
			continue
		}
		milestones.Milestones[idx].FixFeatureIDs = uniqueNonEmptyStrings(append(milestones.Milestones[idx].FixFeatureIDs, added...))
		milestones.Milestones[idx].UpdatedAt = now
	}
	milestones.UpdatedAt = now
	return features, milestones, added
}

func missionFindingAssertionCoverage(finding task.MissionValidationFinding, assertions []task.MissionContractAssertion) []string {
	if len(assertions) == 0 {
		return nil
	}
	var coverage []string
	values := append([]string{}, finding.EvidenceRefs...)
	values = append(values, finding.Summary, finding.RecommendedAction)
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		for _, assertion := range assertions {
			assertionID := strings.TrimSpace(assertion.AssertionID)
			if assertionID == "" {
				continue
			}
			if value == assertionID || strings.Contains(value, assertionID) {
				coverage = append(coverage, assertionID)
				continue
			}
			statement := strings.TrimSpace(assertion.Statement)
			if statement != "" && value == statement {
				coverage = append(coverage, assertionID)
			}
		}
	}
	return uniqueNonEmptyStrings(coverage)
}

func missionAssertionEvidenceFindings(contract task.MissionValidationContract, features task.MissionFeatureSet, milestones task.MissionMilestoneSet) []task.MissionValidationFinding {
	assertions := effectiveMissionAssertions(contract)
	if len(assertions) == 0 {
		return nil
	}
	evidenceByAssertion := make(map[string][]string, len(assertions))
	for _, feature := range features.Features {
		for _, assertion := range assertions {
			if !missionCoverageContainsAssertion(feature.ContractCoverage, assertion) {
				continue
			}
			evidenceByAssertion[assertion.AssertionID] = append(evidenceByAssertion[assertion.AssertionID], missionClosureEvidenceRefs(feature.EvidenceRefs)...)
		}
	}
	for _, milestone := range milestones.Milestones {
		for _, assertion := range assertions {
			if !missionCoverageContainsAssertion(milestone.ContractCoverage, assertion) {
				continue
			}
			evidenceByAssertion[assertion.AssertionID] = append(evidenceByAssertion[assertion.AssertionID], missionClosureEvidenceRefs(milestone.EvidenceRefs)...)
		}
	}
	var findings []task.MissionValidationFinding
	for _, assertion := range assertions {
		if len(uniqueRefs(evidenceByAssertion[assertion.AssertionID])) > 0 {
			continue
		}
		findings = append(findings, missionFinding(
			"missing_assertion_evidence",
			"high",
			true,
			fmt.Sprintf("Validation assertion %s has coverage but no closing root task, worker, verifier, review, completion, or validation evidence ref.", assertion.AssertionID),
			[]string{"validation_contract.json", "features.json", "milestones.json"},
			fmt.Sprintf("Attach durable evidence refs for %s before mission completion.", assertion.AssertionID),
		))
	}
	return findings
}

func missionCoverageContainsAssertion(coverage []string, assertion task.MissionContractAssertion) bool {
	assertionID := strings.TrimSpace(assertion.AssertionID)
	statement := strings.TrimSpace(assertion.Statement)
	for _, item := range coverage {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		if item == assertionID || (statement != "" && item == statement) {
			return true
		}
	}
	return false
}

func missionClosureEvidenceRefs(refs []string) []string {
	var out []string
	for _, ref := range refs {
		ref = strings.TrimSpace(ref)
		if ref == "" {
			continue
		}
		if missionClosureEvidenceRef(ref) {
			out = append(out, ref)
		}
	}
	return out
}

func missionClosureEvidenceRef(ref string) bool {
	switch {
	case strings.Contains(ref, "/state.json"),
		strings.Contains(ref, "/criteria/latest.json"),
		strings.Contains(ref, "/verification/latest.json"),
		strings.Contains(ref, "/reviews/latest.json"),
		strings.Contains(ref, "/completion/latest.json"),
		strings.Contains(ref, "/harness/latest.json"),
		strings.Contains(ref, "/worker_runtime/"),
		strings.Contains(ref, "/command_runs.jsonl#"),
		strings.Contains(ref, "/workspace_edits.jsonl#"),
		strings.HasPrefix(ref, "validation_runs.jsonl#validation_run_id="),
		strings.Contains(ref, "/validation_runs.jsonl#validation_run_id="):
		return true
	default:
		return false
	}
}

func hasBlockingMissionFindings(findings []task.MissionValidationFinding) bool {
	for _, finding := range findings {
		if finding.Blocking {
			return true
		}
	}
	return false
}

func missionBlockingFindingCount(findings []task.MissionValidationFinding) int {
	count := 0
	for _, finding := range findings {
		if finding.Blocking {
			count++
		}
	}
	return count
}

func missionUserTestingSkippedFinding() task.MissionValidationFinding {
	return missionFinding(
		"user_testing_validator_skipped",
		"info",
		false,
		"User-testing validator skipped because no explicit browser, GUI, or computer-use tool plane is configured for this mission.",
		[]string{"mission.json"},
		"Configure a policy-approved user_testing_validator tool plane before expecting browser or GUI validation evidence.",
	)
}

func (s *Service) refreshMissionFeatureRuntimeState(ctx context.Context, mission task.Mission, features task.MissionFeatureSet, milestones task.MissionMilestoneSet) (task.MissionFeatureSet, task.MissionMilestoneSet) {
	now := task.Now()
	featureStatus := make(map[string]string, len(features.Features))
	for idx := range features.Features {
		feature := &features.Features[idx]
		taskID := strings.TrimSpace(feature.BoundTaskID)
		if taskID == "" {
			featureStatus[feature.FeatureID] = feature.Status
			continue
		}
		status, err := s.Status(ctx, taskID)
		if err != nil {
			featureStatus[feature.FeatureID] = feature.Status
			continue
		}
		refs := []string{fmt.Sprintf("workspace:.ngen/tasks/%s/state.json", taskID)}
		switch status.State {
		case task.StateDone:
			feature.Status = task.ProjectStepStatusCompleted
			refs = append(refs, fmt.Sprintf("workspace:.ngen/tasks/%s/completion/latest.json", taskID))
		case task.StateBlocked, task.StateFailed, task.StateAborted:
			feature.Status = task.ProjectStepStatusBlocked
		default:
			feature.Status = task.ProjectStepStatusInProgress
		}
		feature.EvidenceRefs = uniqueRefs(append(feature.EvidenceRefs, refs...))
		feature.UpdatedAt = now
		featureStatus[feature.FeatureID] = feature.Status
	}
	for idx := range milestones.Milestones {
		milestone := &milestones.Milestones[idx]
		ids := uniqueNonEmptyStrings(append(append([]string{}, milestone.FeatureIDs...), milestone.FixFeatureIDs...))
		if len(ids) == 0 {
			continue
		}
		completed := 0
		blocked := 0
		inProgress := 0
		for _, id := range ids {
			switch featureStatus[id] {
			case task.ProjectStepStatusCompleted:
				completed++
			case task.ProjectStepStatusBlocked:
				blocked++
			case task.ProjectStepStatusInProgress:
				inProgress++
			}
		}
		switch {
		case completed == len(ids):
			milestone.Status = task.ProjectStepStatusCompleted
		case blocked > 0:
			milestone.Status = task.ProjectStepStatusBlocked
		case inProgress > 0:
			milestone.Status = task.ProjectStepStatusInProgress
		default:
			milestone.Status = task.ProjectStepStatusPending
		}
		milestone.UpdatedAt = now
	}
	milestones.UpdatedAt = now
	return refreshMissionFeatureSchedule(features, milestones)
}

func refreshMissionFeatureSchedule(features task.MissionFeatureSet, milestones task.MissionMilestoneSet) (task.MissionFeatureSet, task.MissionMilestoneSet) {
	completed := make(map[string]bool, len(features.Features))
	for _, feature := range features.Features {
		if feature.Status == task.ProjectStepStatusCompleted {
			completed[feature.FeatureID] = true
		}
	}
	var current string
	var ready []string
	var blocked []string
	for _, feature := range features.Features {
		if feature.Status == task.ProjectStepStatusInProgress && current == "" {
			current = feature.FeatureID
		}
		if feature.Status != task.ProjectStepStatusPending {
			continue
		}
		depsSatisfied := true
		for _, dep := range feature.DependsOn {
			if dep == "" {
				continue
			}
			if !completed[dep] {
				depsSatisfied = false
				break
			}
		}
		if depsSatisfied {
			ready = append(ready, feature.FeatureID)
			continue
		}
		blocked = append(blocked, feature.FeatureID)
	}
	if current == "" && len(ready) > 0 {
		current = ready[0]
	}
	milestones.CurrentFeatureID = current
	milestones.ReadyFeatureIDs = uniqueNonEmptyStrings(ready)
	milestones.BlockedFeatureIDs = uniqueNonEmptyStrings(blocked)
	return features, milestones
}

func missionFeatureSchedulingFindings(features task.MissionFeatureSet) []task.MissionValidationFinding {
	var active []string
	for _, feature := range features.Features {
		if feature.Status != task.ProjectStepStatusInProgress {
			continue
		}
		if strings.TrimSpace(feature.BoundTaskID) == "" && strings.TrimSpace(feature.BoundWorkerID) == "" {
			continue
		}
		active = append(active, feature.FeatureID)
	}
	if len(active) <= 1 {
		return nil
	}
	return []task.MissionValidationFinding{missionFinding(
		"mission_write_conflict",
		"high",
		true,
		fmt.Sprintf("Mission has %d write-capable features active at once: %s.", len(active), strings.Join(active, ", ")),
		[]string{"features.json", "milestones.json"},
		"Converge one mission feature at a time; keep only read-only reviewer/security/validator work parallel.",
	)}
}

func (s *Service) bindMissionChildTask(ctx context.Context, missionID, childTaskID string) error {
	mission, err := s.Store.LoadMission(missionID)
	if err != nil {
		return err
	}
	child, err := s.Store.LoadTask(childTaskID)
	if err != nil {
		return err
	}
	child = task.HydrateSpec(child, s.Config)
	if strings.TrimSpace(child.RootTaskID) != strings.TrimSpace(mission.RootTaskID) {
		return fmt.Errorf("mission task_create child %s has root_task_id=%s, want %s", child.TaskID, child.RootTaskID, mission.RootTaskID)
	}
	features, err := s.Store.LoadMissionFeatures(missionID)
	if err != nil {
		return err
	}
	milestones, err := s.Store.LoadMissionMilestones(missionID)
	if err != nil {
		return err
	}
	features, milestones = refreshMissionFeatureSchedule(features, milestones)
	featureID := ""
	for _, feature := range features.Features {
		if strings.TrimSpace(feature.BoundTaskID) == child.TaskID {
			featureID = feature.FeatureID
			break
		}
	}
	if featureID == "" {
		featureID = strings.TrimSpace(milestones.CurrentFeatureID)
	}
	if featureID != "" && !missionFeatureCanBindChild(features, featureID, mission.RootTaskID) && !missionFeatureAlreadyBoundToTask(features, featureID, child.TaskID) {
		featureID = ""
		for _, id := range milestones.ReadyFeatureIDs {
			if missionFeatureCanBindChild(features, id, mission.RootTaskID) {
				featureID = id
				break
			}
		}
	}
	if featureID == "" {
		for _, feature := range features.Features {
			if missionFeatureCanBindChild(features, feature.FeatureID, mission.RootTaskID) {
				featureID = feature.FeatureID
				break
			}
		}
	}
	if featureID == "" {
		if existingFeatureID, existingTaskID := missionExistingBoundChildFeature(features, mission.RootTaskID); existingFeatureID != "" {
			return s.appendMissionTaskEvent(mission.RootTaskID, "mission_child_task_created", fmt.Sprintf("Mission feature %s is already bound to child task %s.", existingFeatureID, existingTaskID), []string{
				missionWorkspaceRef(mission.MissionID, "features.json"),
				fmt.Sprintf("workspace:.ngen/tasks/%s/task.json", existingTaskID),
			})
		}
		return fmt.Errorf("mission task_create requires a pending unbound mission feature; root=%s child=%s current=%s ready=%s features=%s", mission.RootTaskID, child.TaskID, milestones.CurrentFeatureID, strings.Join(milestones.ReadyFeatureIDs, ","), missionFeatureBindingSummary(features))
	}
	now := task.Now()
	for idx := range features.Features {
		if features.Features[idx].FeatureID != featureID {
			continue
		}
		features.Features[idx].BoundTaskID = child.TaskID
		features.Features[idx].Status = task.ProjectStepStatusInProgress
		features.Features[idx].EvidenceRefs = uniqueRefs(append(features.Features[idx].EvidenceRefs, fmt.Sprintf("workspace:.ngen/tasks/%s/task.json", child.TaskID)))
		features.Features[idx].UpdatedAt = now
		break
	}
	for idx := range milestones.Milestones {
		if mission.CurrentMilestoneID != "" && milestones.Milestones[idx].MilestoneID != mission.CurrentMilestoneID {
			continue
		}
		milestones.Milestones[idx].BoundTaskIDs = uniqueNonEmptyStrings(append(milestones.Milestones[idx].BoundTaskIDs, child.TaskID))
		milestones.Milestones[idx].FeatureIDs = uniqueNonEmptyStrings(append(milestones.Milestones[idx].FeatureIDs, featureID))
		milestones.Milestones[idx].UpdatedAt = now
	}
	features, milestones = refreshMissionFeatureSchedule(features, milestones)
	if err := s.Store.SaveMissionFeatures(features); err != nil {
		return err
	}
	if err := s.Store.SaveMissionMilestones(milestones); err != nil {
		return err
	}
	if err := s.appendMissionTaskEvent(mission.RootTaskID, "mission_child_task_created", fmt.Sprintf("Created mission child task %s for feature %s.", child.TaskID, featureID), []string{
		missionWorkspaceRef(mission.MissionID, "features.json"),
		fmt.Sprintf("workspace:.ngen/tasks/%s/task.json", child.TaskID),
	}); err != nil {
		return err
	}
	return s.syncMissionRootNarrative(mission.RootTaskID, fmt.Sprintf("Created mission child task %s for feature %s.", child.TaskID, featureID))
}

func missionFeatureCanBindChild(features task.MissionFeatureSet, featureID, rootTaskID string) bool {
	featureID = strings.TrimSpace(featureID)
	if featureID == "" {
		return false
	}
	for _, feature := range features.Features {
		if feature.FeatureID != featureID {
			continue
		}
		boundTaskID := strings.TrimSpace(feature.BoundTaskID)
		if boundTaskID != "" && boundTaskID != strings.TrimSpace(rootTaskID) {
			return false
		}
		if strings.TrimSpace(feature.BoundWorkerID) != "" {
			return false
		}
		return feature.Status == "" || feature.Status == task.ProjectStepStatusPending || feature.Status == task.ProjectStepStatusInProgress
	}
	return false
}

func missionFeatureAlreadyBoundToTask(features task.MissionFeatureSet, featureID, taskID string) bool {
	for _, feature := range features.Features {
		if feature.FeatureID == strings.TrimSpace(featureID) && strings.TrimSpace(feature.BoundTaskID) == strings.TrimSpace(taskID) {
			return true
		}
	}
	return false
}

func missionExistingBoundChildFeature(features task.MissionFeatureSet, rootTaskID string) (string, string) {
	rootTaskID = strings.TrimSpace(rootTaskID)
	for _, feature := range features.Features {
		taskID := strings.TrimSpace(feature.BoundTaskID)
		if taskID == "" || taskID == rootTaskID {
			continue
		}
		return feature.FeatureID, taskID
	}
	return "", ""
}

func missionFeatureBindingSummary(features task.MissionFeatureSet) string {
	parts := make([]string, 0, len(features.Features))
	for _, feature := range features.Features {
		parts = append(parts, fmt.Sprintf("%s:%s:task=%s:worker=%s", feature.FeatureID, feature.Status, feature.BoundTaskID, feature.BoundWorkerID))
	}
	return strings.Join(parts, ",")
}

func (s *Service) scopeMissionFixFeatures(ctx context.Context, mission task.Mission, features task.MissionFeatureSet, featureIDs []string, validationRef string) error {
	byID := make(map[string]task.MissionFeature, len(features.Features))
	for _, feature := range features.Features {
		byID[feature.FeatureID] = feature
	}
	ops := []task.PlanPatchOperation{
		{
			Op:          task.PlanPatchOpSetExplanation,
			Explanation: "Mission validator follow-up work is scoped into the root task execution plan.",
		},
	}
	for _, featureID := range featureIDs {
		feature, ok := byID[featureID]
		if !ok {
			continue
		}
		stepID := fmt.Sprintf("mission:fix:%s", strings.ToLower(feature.FeatureID))
		ops = append(ops, task.PlanPatchOperation{
			Op: task.PlanPatchOpUpsertStep,
			Step: &task.ExecutionPlanStep{
				ID:       stepID,
				Title:    firstNonEmpty(feature.Title, "Resolve mission validator finding"),
				Status:   task.StepStatusPending,
				Priority: task.StepPriorityHigh,
				Notes:    strings.TrimSpace(strings.Join(append([]string{feature.Description}, feature.ValidatorFindingRefs...), " ")),
			},
		})
	}
	if len(ops) <= 1 {
		return nil
	}
	if state, err := s.loadStateOrRecover(mission.RootTaskID); err != nil {
		return err
	} else if state.State == task.StateDone || state.State == task.StateAborted {
		return s.appendMissionTaskEvent(mission.RootTaskID, "mission_fix_scoped", fmt.Sprintf("Scoped %d mission validator follow-up feature(s); root task plan is terminal and unchanged.", len(ops)-1), []string{
			missionWorkspaceRef(mission.MissionID, "features.json"),
			missionWorkspaceRef(mission.MissionID, validationRef),
		})
	}
	if _, err := s.PatchTaskPlan(ctx, mission.RootTaskID, task.PlanPatch{Operations: ops}, "mission_validator"); err != nil {
		return err
	}
	return s.appendMissionTaskEvent(mission.RootTaskID, "mission_fix_scoped", fmt.Sprintf("Scoped %d mission validator follow-up feature(s) into the root task plan.", len(ops)-1), []string{
		missionWorkspaceRef(mission.MissionID, "features.json"),
		missionWorkspaceRef(mission.MissionID, validationRef),
		"workspace:.ngen/tasks/" + mission.RootTaskID + "/plan.json",
	})
}

func (s *Service) appendMissionMetricsRecord(ctx context.Context, missionID, trigger, status string, elapsed time.Duration, refs []string, validatorTimeMS *int64) error {
	mission, err := s.Store.LoadMission(missionID)
	if err != nil {
		return err
	}
	features, _ := s.Store.LoadMissionFeatures(missionID)
	runs, _ := s.Store.ReadMissionValidationRuns(missionID)
	workers, _ := s.ListWorkers(ctx, mission.RootTaskID)
	providerCalls := map[string]int{}
	if events, err := s.Store.ReadEvents(mission.RootTaskID); err == nil {
		for _, event := range events {
			if event.Type == "provider_decided" {
				providerCalls[task.MissionRoleOrchestrator]++
			}
		}
	}
	roleModels := missionRoleModels(mission)
	taskIDs := map[string]struct{}{mission.RootTaskID: {}}
	for _, feature := range features.Features {
		if taskID := strings.TrimSpace(feature.BoundTaskID); taskID != "" {
			taskIDs[taskID] = struct{}{}
		}
	}
	repairAttemptCount := 0
	tokenUsage := "unknown"
	promptCacheUsage := "unknown"
	orderedTaskIDs := make([]string, 0, len(taskIDs))
	for taskID := range taskIDs {
		orderedTaskIDs = append(orderedTaskIDs, taskID)
	}
	sort.Strings(orderedTaskIDs)
	for _, taskID := range orderedTaskIDs {
		if eval, evalErr := s.Store.LoadHarnessEvaluation(taskID); evalErr == nil {
			repairAttemptCount += eval.RepairAttemptCount
			if observedUsage(eval.TokenUsage) {
				tokenUsage = strings.TrimSpace(eval.TokenUsage)
			}
			if observedUsage(eval.PromptCacheUsage) {
				promptCacheUsage = strings.TrimSpace(eval.PromptCacheUsage)
			}
		}
		usageRecords, usageErr := s.Store.ReadProviderUsage(taskID)
		if usageErr != nil {
			return usageErr
		}
		if usageRecord, ok := latestProviderUsage(usageRecords); ok {
			if observedUsage(usageRecord.TokenUsage) {
				tokenUsage = strings.TrimSpace(usageRecord.TokenUsage)
			}
			if observedUsage(usageRecord.PromptCacheUsage) {
				promptCacheUsage = strings.TrimSpace(usageRecord.PromptCacheUsage)
			}
		}
	}
	for idx := len(runs) - 1; idx >= 0; idx-- {
		if observedUsage(runs[idx].TokenUsage) {
			tokenUsage = strings.TrimSpace(runs[idx].TokenUsage)
			break
		}
	}
	for idx := len(runs) - 1; idx >= 0; idx-- {
		if observedUsage(runs[idx].PromptCacheUsage) {
			promptCacheUsage = strings.TrimSpace(runs[idx].PromptCacheUsage)
			break
		}
	}
	record := task.MissionMetricsRecord{
		ObjectKind:          "mission_metrics",
		SchemaVersion:       task.SchemaVersion,
		MetricID:            task.NewID("MMET"),
		MissionID:           mission.MissionID,
		Trigger:             firstNonEmpty(strings.TrimSpace(trigger), "mission"),
		Status:              firstNonEmpty(strings.TrimSpace(status), mission.Status),
		WallTimeMS:          elapsed.Milliseconds(),
		ProviderCallsByRole: providerCalls,
		RoleModels:          roleModels,
		ValidatorTimeMS:     validatorTimeMS,
		TaskCount:           len(taskIDs),
		WorkerCount:         len(workers),
		RepairAttemptCount:  repairAttemptCount,
		ValidationRunCount:  len(runs),
		TokenUsage:          tokenUsage,
		PromptCacheUsage:    promptCacheUsage,
		Cost:                "unknown",
		Refs:                uniqueRefs(refs),
		CreatedAt:           task.Now(),
	}
	return s.Store.AppendMissionMetricsRecord(record)
}

func missionRoleModels(mission task.Mission) map[string]string {
	out := make(map[string]string, len(mission.RolePlan))
	for role, entry := range mission.RolePlan {
		model := strings.TrimSpace(entry.Model)
		if model == "" {
			model = "unknown"
		}
		out[role] = model
	}
	return out
}

func missionMetricsSnapshot(missionID string, records []task.MissionMetricsRecord) *task.MissionMetricsSnapshot {
	if len(records) == 0 {
		return nil
	}
	snapshot := task.MissionMetricsSnapshot{
		ObjectKind:          "mission_metrics_snapshot",
		SchemaVersion:       task.SchemaVersion,
		MissionID:           missionID,
		ProviderCallsByRole: map[string]int{},
		RoleModels:          map[string]string{},
		TokenUsage:          "unknown",
		PromptCacheUsage:    "unknown",
		Cost:                "unknown",
	}
	for _, record := range records {
		snapshot.TotalWallTimeMS += record.WallTimeMS
		if record.ValidatorTimeMS != nil {
			snapshot.TotalValidatorTimeMS += *record.ValidatorTimeMS
		}
		for role, count := range record.ProviderCallsByRole {
			snapshot.ProviderCallsByRole[role] += count
		}
		for role, model := range record.RoleModels {
			snapshot.RoleModels[role] = model
		}
		if record.TaskCount > snapshot.TaskCount {
			snapshot.TaskCount = record.TaskCount
		}
		if record.WorkerCount > snapshot.WorkerCount {
			snapshot.WorkerCount = record.WorkerCount
		}
		if record.RepairAttemptCount > snapshot.RepairAttemptCount {
			snapshot.RepairAttemptCount = record.RepairAttemptCount
		}
		if record.ValidationRunCount > snapshot.ValidationRunCount {
			snapshot.ValidationRunCount = record.ValidationRunCount
		}
		if observedUsage(record.TokenUsage) {
			snapshot.TokenUsage = strings.TrimSpace(record.TokenUsage)
		}
		if observedUsage(record.PromptCacheUsage) {
			snapshot.PromptCacheUsage = strings.TrimSpace(record.PromptCacheUsage)
		}
		if observedUsage(record.Cost) {
			snapshot.Cost = strings.TrimSpace(record.Cost)
		}
		snapshot.LatestMetricRef = fmt.Sprintf("metrics.jsonl#metric_id=%s", record.MetricID)
	}
	return &snapshot
}

func (s *Service) missionStatusSnapshot(mission task.Mission, features task.MissionFeatureSet, milestones task.MissionMilestoneSet, latest *task.MissionValidationRun, rootStatus *task.StatusSnapshot, metrics *task.MissionMetricsSnapshot) task.MissionStatusSnapshot {
	snapshot := task.MissionStatusSnapshot{
		ObjectKind:                 "mission_status_snapshot",
		SchemaVersion:              task.SchemaVersion,
		MissionID:                  mission.MissionID,
		Status:                     mission.Status,
		StatusReasonCode:           mission.StatusReasonCode,
		CurrentMilestoneID:         mission.CurrentMilestoneID,
		CurrentFeatureID:           milestones.CurrentFeatureID,
		ReadyFeatureIDs:            append([]string(nil), milestones.ReadyFeatureIDs...),
		BlockedFeatureIDs:          append([]string(nil), milestones.BlockedFeatureIDs...),
		RootTaskID:                 mission.RootTaskID,
		Metrics:                    metrics,
		UserTestingValidatorStatus: "skipped",
		UserTestingValidatorReason: "no explicit browser, GUI, or computer-use tool plane is configured",
	}
	if rootStatus != nil {
		snapshot.RootTaskState = string(rootStatus.State)
	}
	if latest != nil {
		snapshot.LatestValidationRef = mission.LatestValidationRef
		snapshot.LatestValidationStatus = latest.Status
		snapshot.BlockingFindingCount = missionBlockingFindingCount(latest.Findings)
	}
	for _, feature := range features.Features {
		if len(feature.ValidatorFindingRefs) > 0 && feature.Status != task.ProjectStepStatusCompleted {
			snapshot.UnresolvedFixFeatureIDs = append(snapshot.UnresolvedFixFeatureIDs, feature.FeatureID)
		}
		if feature.Status != task.ProjectStepStatusInProgress {
			continue
		}
		if taskID := strings.TrimSpace(feature.BoundTaskID); taskID != "" {
			snapshot.ActiveTaskIDs = append(snapshot.ActiveTaskIDs, taskID)
		}
		if workerID := strings.TrimSpace(feature.BoundWorkerID); workerID != "" {
			snapshot.ActiveWorkerIDs = append(snapshot.ActiveWorkerIDs, workerID)
		}
	}
	if events, err := s.Store.ReadEvents(mission.RootTaskID); err == nil {
		for idx := len(events) - 1; idx >= 0 && len(snapshot.RecentMissionEvents) < 5; idx-- {
			if !strings.HasPrefix(events[idx].Type, "mission_") {
				continue
			}
			snapshot.RecentMissionEvents = append(snapshot.RecentMissionEvents, fmt.Sprintf("%s: %s", events[idx].Type, events[idx].Summary))
		}
	}
	snapshot.ActiveTaskIDs = uniqueNonEmptyStrings(snapshot.ActiveTaskIDs)
	snapshot.ActiveWorkerIDs = uniqueNonEmptyStrings(snapshot.ActiveWorkerIDs)
	snapshot.UnresolvedFixFeatureIDs = uniqueNonEmptyStrings(snapshot.UnresolvedFixFeatureIDs)
	return snapshot
}

func (s *Service) RunMission(ctx context.Context, missionID string) (view task.MissionView, err error) {
	started := time.Now()
	metricsMissionID := ""
	defer func() {
		if metricsMissionID == "" {
			return
		}
		status := "error"
		if view.Mission.Status != "" {
			status = view.Mission.Status
		}
		metricErr := s.appendMissionMetricsRecord(ctx, metricsMissionID, "mission_run", status, time.Since(started), []string{"mission.json", "features.json", "milestones.json"}, nil)
		if metricErr == nil && view.Mission.RootTaskID != "" {
			metricErr = s.syncMissionRootNarrative(view.Mission.RootTaskID, fmt.Sprintf("Recorded mission %s run metrics.", metricsMissionID))
		}
		if metricErr == nil {
			return
		}
		if err == nil {
			err = metricErr
			return
		}
		err = fmt.Errorf("%v; record mission metrics: %w", err, metricErr)
	}()
	mission, err := s.Store.LoadMission(missionID)
	if err != nil {
		return task.MissionView{}, err
	}
	metricsMissionID = mission.MissionID
	if mission.Status == task.MissionStatusPaused {
		return task.MissionView{}, fmt.Errorf("mission is paused")
	}
	if mission.Status == task.MissionStatusDone {
		return s.GetMission(ctx, missionID)
	}
	contract, err := s.Store.LoadMissionValidationContract(missionID)
	if err != nil {
		return task.MissionView{}, err
	}
	features, err := s.Store.LoadMissionFeatures(missionID)
	if err != nil {
		return task.MissionView{}, err
	}
	milestones, err := s.Store.LoadMissionMilestones(missionID)
	if err != nil {
		return task.MissionView{}, err
	}
	features, milestones = s.refreshMissionFeatureRuntimeState(ctx, mission, features, milestones)
	if err := s.Store.SaveMissionFeatures(features); err != nil {
		return task.MissionView{}, err
	}
	if err := s.Store.SaveMissionMilestones(milestones); err != nil {
		return task.MissionView{}, err
	}
	if findings := missionPlanGateFindings(mission, contract, features, milestones, true); len(findings) > 0 {
		run := task.MissionValidationRun{
			ObjectKind:            "mission_validation_run",
			SchemaVersion:         task.SchemaVersion,
			ValidationRunID:       task.NewID("MVAL"),
			MissionID:             mission.MissionID,
			MilestoneID:           mission.CurrentMilestoneID,
			RootTaskID:            mission.RootTaskID,
			ValidatorRole:         task.MissionRoleOrchestrator,
			ValidatorKind:         "deterministic_plan_gate",
			Status:                "blocking",
			Summary:               fmt.Sprintf("Mission run blocked by %d plan gate finding(s).", len(findings)),
			ContractCoverageCount: len(effectiveMissionAssertions(contract)),
			Findings:              findings,
			EvidenceRefs:          []string{"mission.json", "validation_contract.json", "features.json", "milestones.json"},
			ValidatorContextRefs:  []string{"mission.json", "validation_contract.json", "features.json", "milestones.json"},
			CreatedAt:             task.Now(),
		}
		view, err = s.persistMissionPreflightBlock(ctx, mission, run, "blocked_plan_gate")
		return view, err
	}
	mission.Status = task.MissionStatusActive
	mission.StatusReasonCode = ""
	mission.UpdatedAt = task.Now()
	if err := s.Store.SaveMission(mission); err != nil {
		return task.MissionView{}, err
	}
	if err := s.runMissionOrchestrationPass(ctx, mission); err != nil {
		return task.MissionView{}, err
	}
	view, err = s.ValidateMission(ctx, mission.MissionID, mission.CurrentMilestoneID)
	return view, err
}

func (s *Service) runMissionOrchestrationPass(ctx context.Context, mission task.Mission) error {
	scoped, _, err := s.serviceForMissionRole(mission, task.MissionRoleOrchestrator)
	if err != nil {
		return err
	}
	_, _, err = scoped.autoWithOptions(ctx, mission.RootTaskID, nil, autoOptions{
		RuntimeAction: "mission_orchestrator",
		MissionID:     mission.MissionID,
	})
	return err
}

func (s *Service) PauseMission(ctx context.Context, missionID, reason string) (task.MissionView, error) {
	_ = ctx
	mission, err := s.Store.LoadMission(missionID)
	if err != nil {
		return task.MissionView{}, err
	}
	mission.Status = task.MissionStatusPaused
	mission.StatusReasonCode = firstNonEmpty(strings.TrimSpace(reason), "paused_operator")
	mission.UpdatedAt = task.Now()
	if err := s.Store.SaveMission(mission); err != nil {
		return task.MissionView{}, err
	}
	if err := s.appendMissionTaskEvent(mission.RootTaskID, "mission_paused", fmt.Sprintf("Paused mission %s: %s", mission.MissionID, mission.StatusReasonCode), []string{missionWorkspaceRef(mission.MissionID, "mission.json")}); err != nil {
		return task.MissionView{}, err
	}
	if err := s.syncMissionRootNarrative(mission.RootTaskID, fmt.Sprintf("Paused mission %s: %s", mission.MissionID, mission.StatusReasonCode)); err != nil {
		return task.MissionView{}, err
	}
	return s.missionView(missionID)
}

func (s *Service) ResumeMission(ctx context.Context, missionID string) (task.MissionView, error) {
	_ = ctx
	mission, err := s.Store.LoadMission(missionID)
	if err != nil {
		return task.MissionView{}, err
	}
	if mission.Status != task.MissionStatusDone {
		if missionPlanApprovalRecorded(mission) {
			mission.Status = task.MissionStatusActive
			mission.StatusReasonCode = ""
		} else {
			mission.Status = task.MissionStatusDraft
			mission.StatusReasonCode = "awaiting_plan_approval"
		}
		mission.UpdatedAt = task.Now()
		if err := s.Store.SaveMission(mission); err != nil {
			return task.MissionView{}, err
		}
		if err := s.appendMissionTaskEvent(mission.RootTaskID, "mission_resumed", fmt.Sprintf("Resumed mission %s.", mission.MissionID), []string{missionWorkspaceRef(mission.MissionID, "mission.json")}); err != nil {
			return task.MissionView{}, err
		}
		if err := s.syncMissionRootNarrative(mission.RootTaskID, fmt.Sprintf("Resumed mission %s.", mission.MissionID)); err != nil {
			return task.MissionView{}, err
		}
	}
	return s.missionView(missionID)
}

func (s *Service) OpenMissionForTask(ctx context.Context, taskID string) (task.MissionView, error) {
	if missionID, err := s.missionIDForTask(taskID); err != nil {
		return task.MissionView{}, err
	} else if missionID != "" {
		return s.GetMission(ctx, missionID)
	}
	spec, err := s.Store.LoadTask(taskID)
	if err != nil {
		return task.MissionView{}, err
	}
	criteria := make([]string, 0, len(spec.SuccessCriteria))
	for _, criterion := range spec.SuccessCriteria {
		if statement := strings.TrimSpace(criterion.Statement); statement != "" {
			criteria = append(criteria, statement)
		}
	}
	return s.CreateMission(ctx, task.MissionCreateRequest{
		Title:      firstNonEmpty(spec.Title, missionTitleFromObjective(spec.Objective)),
		Objective:  spec.Objective,
		RootTaskID: spec.TaskID,
		Criteria:   criteria,
	})
}

func (s *Service) OpenOrSetMissionForTask(ctx context.Context, taskID string, req task.MissionCreateRequest) (task.MissionView, error) {
	taskID = strings.TrimSpace(taskID)
	req.Title = strings.TrimSpace(req.Title)
	req.Objective = strings.TrimSpace(req.Objective)
	req.Criteria = normalizeMissionStrings(req.Criteria)
	req.Constraints = normalizeMissionStrings(req.Constraints)
	if req.Objective == "" {
		return s.OpenMissionForTask(ctx, taskID)
	}
	if req.Title == "" {
		req.Title = missionTitleFromObjective(req.Objective)
	}
	if len(req.Criteria) == 0 {
		req.Criteria = []string{"mission objective is satisfied with evidence"}
	}
	missionID, err := s.missionIDForTask(taskID)
	if err != nil {
		return task.MissionView{}, err
	}
	if missionID == "" {
		req.RootTaskID = taskID
		return s.CreateMission(ctx, req)
	}
	return s.setMissionObjective(ctx, missionID, req)
}

func (s *Service) setMissionObjective(ctx context.Context, missionID string, req task.MissionCreateRequest) (task.MissionView, error) {
	mission, err := s.Store.LoadMission(missionID)
	if err != nil {
		return task.MissionView{}, err
	}
	now := task.Now()
	mission.Title = req.Title
	mission.Objective = req.Objective
	mission.Status = task.MissionStatusDraft
	mission.StatusReasonCode = "awaiting_plan_approval"
	mission.PlanApprovalStatus = task.MissionPlanApprovalPending
	mission.PlanApprovedAt = ""
	mission.PlanApprovedBy = ""
	mission.PlanApprovedContractRef = ""
	mission.CurrentMilestoneID = "MS-001"
	mission.LatestValidationRef = ""
	mission.FeatureRefs = []string{"features.json#feature_id=FEAT-001"}
	mission.MilestoneRefs = []string{"milestones.json#milestone_id=MS-001"}
	rolePlan, err := s.missionRolePlan()
	if err != nil {
		return task.MissionView{}, err
	}
	mission.RolePlan = rolePlan
	mission.UpdatedAt = now

	contract, err := s.Store.LoadMissionValidationContract(missionID)
	if err != nil {
		return task.MissionView{}, err
	}
	evidenceRequirements := contract.EvidenceRequirements
	if len(evidenceRequirements) == 0 {
		evidenceRequirements = []string{
			"Root task reaches Done through the existing verifier/review/completion gate.",
			"Open criteria count is zero in criteria/latest.json.",
			"Latest completion report is accepted.",
		}
	}
	assertions := missionContractAssertions(req.Criteria, evidenceRequirements)
	assertionIDs := missionAssertionIDs(assertions)
	contract.BehavioralRequirements = append([]string(nil), req.Criteria...)
	contract.AcceptanceTests = append([]string(nil), req.Criteria...)
	contract.Assertions = assertions
	contract.NegativeCases = missionNegativeCases(req.Criteria)
	contract.ManualChecks = missionManualChecks(req.Criteria)
	contract.EvidenceRequirements = evidenceRequirements
	contract.UpdatedAt = now

	taskRef := fmt.Sprintf("workspace:.ngen/tasks/%s/task.json", mission.RootTaskID)
	features := task.MissionFeatureSet{
		ObjectKind:    "mission_features",
		SchemaVersion: task.SchemaVersion,
		MissionID:     missionID,
		Features: []task.MissionFeature{
			{
				FeatureID:        "FEAT-001",
				Title:            req.Title,
				Description:      req.Objective,
				BoundTaskID:      mission.RootTaskID,
				ContractCoverage: append([]string(nil), assertionIDs...),
				Status:           task.ProjectStepStatusPending,
				EvidenceRefs:     []string{taskRef},
				UpdatedAt:        now,
			},
		},
		UpdatedAt: now,
	}
	milestones := task.MissionMilestoneSet{
		ObjectKind:       "mission_milestones",
		SchemaVersion:    task.SchemaVersion,
		MissionID:        missionID,
		CurrentFeatureID: "FEAT-001",
		ReadyFeatureIDs:  []string{"FEAT-001"},
		Milestones: []task.MissionMilestone{
			{
				MilestoneID:      "MS-001",
				Title:            req.Title,
				FeatureIDs:       []string{"FEAT-001"},
				BoundTaskIDs:     []string{mission.RootTaskID},
				ContractCoverage: append([]string(nil), assertionIDs...),
				Status:           task.ProjectStepStatusPending,
				EvidenceRefs:     []string{taskRef},
				UpdatedAt:        now,
			},
		},
		UpdatedAt: now,
	}
	if err := s.Store.SaveMission(mission); err != nil {
		return task.MissionView{}, err
	}
	if err := s.Store.SaveMissionValidationContract(contract); err != nil {
		return task.MissionView{}, err
	}
	if err := s.Store.SaveMissionFeatures(features); err != nil {
		return task.MissionView{}, err
	}
	if err := s.Store.SaveMissionMilestones(milestones); err != nil {
		return task.MissionView{}, err
	}
	if err := s.saveMissionNotes(mission, contract); err != nil {
		return task.MissionView{}, err
	}
	if err := s.appendMissionTaskEvent(mission.RootTaskID, "mission_updated", fmt.Sprintf("Updated mission %s objective.", missionID), []string{missionWorkspaceRef(missionID, "mission.json"), missionWorkspaceRef(missionID, "validation_contract.json")}); err != nil {
		return task.MissionView{}, err
	}
	if err := s.syncMissionRootNarrative(mission.RootTaskID, fmt.Sprintf("Updated mission %s objective.", missionID)); err != nil {
		return task.MissionView{}, err
	}
	return s.GetMission(ctx, missionID)
}

func (s *Service) missionIDForTask(taskID string) (string, error) {
	taskID = strings.TrimSpace(taskID)
	if taskID == "" {
		return "", nil
	}
	var candidateRootTaskID string
	if spec, err := s.Store.LoadTask(taskID); err == nil {
		spec = task.HydrateSpec(spec, s.Config)
		candidateRootTaskID = strings.TrimSpace(spec.RootTaskID)
	}
	ids, err := s.Store.ListMissionIDs()
	if err != nil {
		return "", err
	}
	for _, id := range ids {
		mission, loadErr := s.Store.LoadMission(id)
		if loadErr != nil {
			return "", loadErr
		}
		missionRootTaskID := strings.TrimSpace(mission.RootTaskID)
		if missionRootTaskID == taskID || (candidateRootTaskID != "" && missionRootTaskID == candidateRootTaskID) {
			return id, nil
		}
	}
	return "", nil
}

func (s *Service) missionForOwnedTaskSpec(spec task.Spec) (task.Mission, bool, error) {
	spec = task.HydrateSpec(spec, s.Config)
	taskID := strings.TrimSpace(spec.TaskID)
	rootTaskID := strings.TrimSpace(spec.RootTaskID)
	if rootTaskID == "" {
		rootTaskID = taskID
	}
	ids, err := s.Store.ListMissionIDs()
	if err != nil {
		return task.Mission{}, false, err
	}
	for _, id := range ids {
		mission, loadErr := s.Store.LoadMission(id)
		if loadErr != nil {
			return task.Mission{}, false, loadErr
		}
		missionRootTaskID := strings.TrimSpace(mission.RootTaskID)
		if missionRootTaskID == taskID || missionRootTaskID == rootTaskID {
			return mission, true, nil
		}
	}
	return task.Mission{}, false, nil
}

func (s *Service) providerMissionViewForTask(taskID string) (*task.MissionView, error) {
	missionID, err := s.missionIDForTask(taskID)
	if err != nil {
		return nil, err
	}
	if missionID == "" {
		return nil, nil
	}
	view, err := s.missionView(missionID)
	if err != nil {
		return nil, err
	}
	return &view, nil
}

func (s *Service) statusMissionForTask(taskID string) (*task.Mission, error) {
	missionID, err := s.missionIDForTask(taskID)
	if err != nil {
		return nil, err
	}
	if missionID == "" {
		return nil, nil
	}
	mission, err := s.Store.LoadMission(missionID)
	if err != nil {
		return nil, err
	}
	return &mission, nil
}

func (s *Service) missionRefsForTask(taskID string) ([]string, error) {
	missionID, err := s.missionIDForTask(taskID)
	if err != nil || missionID == "" {
		return nil, err
	}
	refs := []string{
		missionWorkspaceRef(missionID, "mission.json"),
		missionWorkspaceRef(missionID, "validation_contract.json"),
		missionWorkspaceRef(missionID, "features.json"),
		missionWorkspaceRef(missionID, "milestones.json"),
	}
	mission, err := s.Store.LoadMission(missionID)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(mission.LatestValidationRef) != "" {
		refs = append(refs, missionWorkspaceRef(missionID, mission.LatestValidationRef))
	}
	return refs, nil
}

func (s *Service) syncMissionRootNarrative(taskID, summary string) error {
	spec, err := s.Store.LoadTask(taskID)
	if err != nil {
		return err
	}
	spec = task.HydrateSpec(spec, s.Config)
	state, err := s.loadStateOrRecover(taskID)
	if err != nil {
		return err
	}
	return s.syncTaskNarrative(spec, state, summary)
}

func (s *Service) missionView(missionID string) (task.MissionView, error) {
	mission, err := s.Store.LoadMission(missionID)
	if err != nil {
		return task.MissionView{}, err
	}
	contract, err := s.Store.LoadMissionValidationContract(missionID)
	if err != nil {
		return task.MissionView{}, err
	}
	features, err := s.Store.LoadMissionFeatures(missionID)
	if err != nil {
		return task.MissionView{}, err
	}
	milestones, err := s.Store.LoadMissionMilestones(missionID)
	if err != nil {
		return task.MissionView{}, err
	}
	var latest *task.MissionValidationRun
	if runs, err := s.Store.ReadMissionValidationRuns(missionID); err != nil {
		return task.MissionView{}, err
	} else if len(runs) > 0 {
		latestRefID := strings.TrimPrefix(strings.TrimSpace(mission.LatestValidationRef), "validation_runs.jsonl#validation_run_id=")
		if latestRefID != "" {
			for idx := len(runs) - 1; idx >= 0; idx-- {
				if runs[idx].ValidationRunID == latestRefID {
					latestRun := runs[idx]
					latest = &latestRun
					break
				}
			}
		} else if mission.Status == task.MissionStatusBlocked || mission.Status == task.MissionStatusDone {
			latestRun := runs[len(runs)-1]
			latest = &latestRun
		}
	}
	var rootStatus *task.StatusSnapshot
	if status, err := s.Status(context.Background(), mission.RootTaskID); err == nil {
		rootStatus = &status
	}
	var metrics *task.MissionMetricsSnapshot
	if records, err := s.Store.ReadMissionMetrics(missionID); err == nil {
		metrics = missionMetricsSnapshot(missionID, records)
	}
	statusSnapshot := s.missionStatusSnapshot(mission, features, milestones, latest, rootStatus, metrics)
	return task.MissionView{
		ObjectKind:            "mission_view",
		SchemaVersion:         task.SchemaVersion,
		Mission:               mission,
		Contract:              contract,
		Features:              features,
		Milestones:            milestones,
		LatestValidation:      latest,
		RootTaskStatus:        rootStatus,
		MissionStatusSnapshot: &statusSnapshot,
		Metrics:               metrics,
	}, nil
}

func (s *Service) appendMissionTaskEvent(taskID, eventType, summary string, refs []string) error {
	state, err := s.loadStateOrRecover(taskID)
	if err != nil {
		return err
	}
	event := newEvent(taskID, state, eventType, summary, refs)
	if err := s.Store.AppendEvent(event); err != nil {
		return err
	}
	state.LastEventRef = artifact.EventRef(event.EventID)
	state.UpdatedAt = task.Now()
	return s.Store.SaveState(state)
}

func (s *Service) saveMissionNotes(mission task.Mission, contract task.MissionValidationContract) error {
	var b strings.Builder
	fmt.Fprintf(&b, "# Mission %s\n\n", mission.MissionID)
	fmt.Fprintf(&b, "## Objective\n\n%s\n\n", mission.Objective)
	fmt.Fprintf(&b, "## Root Task\n\n- `%s`\n\n", mission.RootTaskID)
	fmt.Fprintf(&b, "## Plan Approval\n\n- status: `%s`\n", firstNonEmpty(mission.PlanApprovalStatus, task.MissionPlanApprovalPending))
	if strings.TrimSpace(mission.PlanApprovedContractRef) != "" {
		fmt.Fprintf(&b, "- approved contract: `%s`\n", mission.PlanApprovedContractRef)
	}
	fmt.Fprintf(&b, "\n")
	fmt.Fprintf(&b, "## Validation Contract\n\n")
	for _, assertion := range effectiveMissionAssertions(contract) {
		fmt.Fprintf(&b, "- `%s` %s\n", assertion.AssertionID, assertion.Statement)
	}
	return s.Store.SaveMissionNotes(mission.MissionID, []byte(b.String()))
}

func (s *Service) renderMissionMarkdown(b *strings.Builder, taskID string) bool {
	missionID, err := s.missionIDForTask(taskID)
	if err != nil {
		fmt.Fprintf(b, "\n## Mission\n")
		fmt.Fprintf(b, "- Mission lookup failed: %v\n", err)
		return true
	}
	if missionID == "" {
		return false
	}
	fmt.Fprintf(b, "\n## Mission\n")
	mission, err := s.Store.LoadMission(missionID)
	if err != nil {
		fmt.Fprintf(b, "- Mission Ref: %s\n", missionWorkspaceRef(missionID, "mission.json"))
		fmt.Fprintf(b, "- Mission artifact could not be loaded: %v\n", err)
		return true
	}
	fmt.Fprintf(b, "- Mission Ref: %s\n", missionWorkspaceRef(mission.MissionID, "mission.json"))
	fmt.Fprintf(b, "- Status: %s\n", mission.Status)
	if mission.StatusReasonCode != "" {
		fmt.Fprintf(b, "- Status Reason: %s\n", mission.StatusReasonCode)
	}
	fmt.Fprintf(b, "- Plan Approval: %s\n", firstNonEmpty(mission.PlanApprovalStatus, task.MissionPlanApprovalPending))
	if strings.TrimSpace(mission.PlanApprovedContractRef) != "" {
		fmt.Fprintf(b, "- Approved Contract Ref: %s\n", missionWorkspaceRef(mission.MissionID, mission.PlanApprovedContractRef))
	}
	fmt.Fprintf(b, "- Current Milestone: %s\n", mission.CurrentMilestoneID)
	fmt.Fprintf(b, "- Validation Contract Ref: %s\n", missionWorkspaceRef(mission.MissionID, mission.ValidationContractRef))
	if contract, err := s.Store.LoadMissionValidationContract(mission.MissionID); err == nil {
		fmt.Fprintf(b, "- Contract Coverage: %d assertion(s), %d evidence requirement(s)\n", len(effectiveMissionAssertions(contract)), len(contract.EvidenceRequirements))
	} else {
		fmt.Fprintf(b, "- Validation contract could not be loaded: %v\n", err)
	}
	if features, err := s.Store.LoadMissionFeatures(mission.MissionID); err == nil {
		completed := 0
		for _, feature := range features.Features {
			if feature.Status == task.ProjectStepStatusCompleted {
				completed++
			}
		}
		fmt.Fprintf(b, "- Feature Coverage: %d/%d completed\n", completed, len(features.Features))
	}
	if milestones, err := s.Store.LoadMissionMilestones(mission.MissionID); err == nil {
		completed := 0
		for _, milestone := range milestones.Milestones {
			if milestone.Status == task.ProjectStepStatusCompleted {
				completed++
			}
		}
		fmt.Fprintf(b, "- Milestone Coverage: %d/%d completed\n", completed, len(milestones.Milestones))
	}
	if strings.TrimSpace(mission.LatestValidationRef) != "" {
		fmt.Fprintf(b, "- Latest Validation Ref: %s\n", missionWorkspaceRef(mission.MissionID, mission.LatestValidationRef))
	}
	if runs, err := s.Store.ReadMissionValidationRuns(mission.MissionID); err == nil && len(runs) > 0 {
		latest := runs[len(runs)-1]
		fmt.Fprintf(b, "- Latest Validation Status: %s\n", latest.Status)
		if strings.TrimSpace(latest.Summary) != "" {
			fmt.Fprintf(b, "- Latest Validation Summary: %s\n", strings.TrimSpace(latest.Summary))
		}
	}
	if records, err := s.Store.ReadMissionMetrics(mission.MissionID); err == nil {
		if metrics := missionMetricsSnapshot(mission.MissionID, records); metrics != nil {
			fmt.Fprintf(b, "- Metrics: wall_ms=%d validator_ms=%d tasks=%d workers=%d repairs=%d validations=%d tokens=%s cache=%s cost=%s\n", metrics.TotalWallTimeMS, metrics.TotalValidatorTimeMS, metrics.TaskCount, metrics.WorkerCount, metrics.RepairAttemptCount, metrics.ValidationRunCount, firstNonEmpty(metrics.TokenUsage, "unknown"), firstNonEmpty(metrics.PromptCacheUsage, "unknown"), firstNonEmpty(metrics.Cost, "unknown"))
		}
	}
	return true
}

func missionFinding(category, severity string, blocking bool, summary string, refs []string, action string) task.MissionValidationFinding {
	return task.MissionValidationFinding{
		FindingID:         task.NewID("MFIND"),
		Category:          category,
		Severity:          severity,
		Blocking:          blocking,
		Summary:           summary,
		EvidenceRefs:      uniqueRefs(refs),
		RecommendedAction: action,
	}
}

func missionContractAssertions(criteria, evidenceRequirements []string) []task.MissionContractAssertion {
	criteria = normalizeMissionStrings(criteria)
	evidenceRequirements = normalizeMissionStrings(evidenceRequirements)
	assertions := make([]task.MissionContractAssertion, 0, len(criteria))
	for idx, criterion := range criteria {
		assertions = append(assertions, task.MissionContractAssertion{
			AssertionID:      fmt.Sprintf("ASSERT-%03d", idx+1),
			Kind:             "acceptance",
			Statement:        criterion,
			EvidenceRequired: append([]string(nil), evidenceRequirements...),
			Validator:        "deterministic_artifact",
			NegativeCase:     fmt.Sprintf("Do not accept mission completion without evidence for: %s", criterion),
			ManualCheck:      fmt.Sprintf("Inspect mission evidence refs for assertion %s before relying on this result.", fmt.Sprintf("ASSERT-%03d", idx+1)),
		})
	}
	return assertions
}

func missionNegativeCases(criteria []string) []string {
	criteria = normalizeMissionStrings(criteria)
	out := make([]string, 0, len(criteria))
	for _, criterion := range criteria {
		out = append(out, fmt.Sprintf("Do not mark the mission Done without artifact-backed evidence for: %s", criterion))
	}
	return out
}

func missionManualChecks(criteria []string) []string {
	criteria = normalizeMissionStrings(criteria)
	out := make([]string, 0, len(criteria))
	for _, criterion := range criteria {
		out = append(out, fmt.Sprintf("Operator may manually inspect cited evidence refs for: %s", criterion))
	}
	return out
}

func missionAssertionIDs(assertions []task.MissionContractAssertion) []string {
	ids := make([]string, 0, len(assertions))
	for _, assertion := range assertions {
		if id := strings.TrimSpace(assertion.AssertionID); id != "" {
			ids = append(ids, id)
		}
	}
	return uniqueNonEmptyStrings(ids)
}

func effectiveMissionAssertions(contract task.MissionValidationContract) []task.MissionContractAssertion {
	if len(contract.Assertions) > 0 {
		out := make([]task.MissionContractAssertion, 0, len(contract.Assertions))
		for _, assertion := range contract.Assertions {
			assertion.AssertionID = strings.TrimSpace(assertion.AssertionID)
			assertion.Statement = strings.TrimSpace(assertion.Statement)
			if assertion.AssertionID == "" && assertion.Statement != "" {
				assertion.AssertionID = fmt.Sprintf("ASSERT-%03d", len(out)+1)
			}
			if assertion.Statement == "" {
				continue
			}
			if assertion.Kind == "" {
				assertion.Kind = "acceptance"
			}
			out = append(out, assertion)
		}
		return out
	}
	return missionContractAssertions(contract.AcceptanceTests, contract.EvidenceRequirements)
}

func missionPlanApprovalRecorded(mission task.Mission) bool {
	return strings.TrimSpace(mission.PlanApprovalStatus) == task.MissionPlanApprovalApproved && strings.TrimSpace(mission.PlanApprovedContractRef) != ""
}

func missionApprovedContractRef(contract task.MissionValidationContract) string {
	return fmt.Sprintf("validation_contract.json#contract_id=%s", strings.TrimSpace(contract.ContractID))
}

func missionPlanApprovedForContract(mission task.Mission, contract task.MissionValidationContract) bool {
	if strings.TrimSpace(contract.ContractID) == "" {
		return false
	}
	expected := missionApprovedContractRef(contract)
	return missionPlanApprovalRecorded(mission) && strings.TrimSpace(mission.PlanApprovedContractRef) == expected
}

func missionPlanGateFindings(mission task.Mission, contract task.MissionValidationContract, features task.MissionFeatureSet, milestones task.MissionMilestoneSet, requireApproval bool) []task.MissionValidationFinding {
	findings := missionContractCoverageFindings(contract, features, milestones)
	findings = append(findings, missionFeatureSchedulingFindings(features)...)
	if requireApproval && !missionPlanApprovedForContract(mission, contract) {
		summary := "Mission plan is not approved; approve the validation contract before mission execution or completion."
		if missionPlanApprovalRecorded(mission) {
			summary = "Mission plan approval does not match the current validation contract; re-approve the current contract before mission execution or completion."
		}
		findings = append(findings, missionFinding(
			"plan_unapproved",
			"high",
			true,
			summary,
			[]string{"mission.json", "validation_contract.json", "features.json", "milestones.json"},
			"Run `ngen mission approve <mission_id>` after reviewing validation_contract.json, features.json, and milestones.json.",
		))
	}
	return findings
}

func missionContractCoverageFindings(contract task.MissionValidationContract, features task.MissionFeatureSet, milestones task.MissionMilestoneSet) []task.MissionValidationFinding {
	assertions := effectiveMissionAssertions(contract)
	if len(assertions) == 0 {
		return []task.MissionValidationFinding{missionFinding(
			"missing_contract_assertions",
			"critical",
			true,
			"Validation contract has no assertions or acceptance tests.",
			[]string{"validation_contract.json"},
			"Add at least one validation contract assertion before approving or running the mission.",
		)}
	}
	featureCoverage := make(map[string]bool, len(assertions))
	for _, feature := range features.Features {
		markMissionCoverage(featureCoverage, assertions, feature.ContractCoverage)
	}
	milestoneCoverage := make(map[string]bool, len(assertions))
	for _, milestone := range milestones.Milestones {
		markMissionCoverage(milestoneCoverage, assertions, milestone.ContractCoverage)
	}
	var findings []task.MissionValidationFinding
	for _, assertion := range assertions {
		if !featureCoverage[assertion.AssertionID] {
			findings = append(findings, missionFinding(
				"uncovered_assertion",
				"high",
				true,
				fmt.Sprintf("Validation assertion %s is not covered by any mission feature.", assertion.AssertionID),
				[]string{"validation_contract.json", "features.json"},
				fmt.Sprintf("Add %s to a feature contract_coverage entry before approving or running the mission.", assertion.AssertionID),
			))
		}
		if !milestoneCoverage[assertion.AssertionID] {
			findings = append(findings, missionFinding(
				"uncovered_assertion",
				"high",
				true,
				fmt.Sprintf("Validation assertion %s is not covered by any mission milestone.", assertion.AssertionID),
				[]string{"validation_contract.json", "milestones.json"},
				fmt.Sprintf("Add %s to a milestone contract_coverage entry before approving or running the mission.", assertion.AssertionID),
			))
		}
	}
	return findings
}

func markMissionCoverage(covered map[string]bool, assertions []task.MissionContractAssertion, coverage []string) {
	for _, item := range coverage {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		for _, assertion := range assertions {
			if item == assertion.AssertionID || item == assertion.Statement {
				covered[assertion.AssertionID] = true
			}
		}
	}
}

func missionFindingCreatesFixFeature(finding task.MissionValidationFinding) bool {
	switch strings.TrimSpace(finding.Category) {
	case "plan_unapproved", "uncovered_assertion", "missing_contract_assertions", "missing_root_status", "missing_evidence", "missing_criteria", "open_criteria", "missing_completion", "completion_not_accepted", "missing_assertion_evidence", "mission_write_conflict":
		return false
	default:
		return true
	}
}

func markMissionSets(features task.MissionFeatureSet, milestones task.MissionMilestoneSet, milestoneID, status, validationRef string, evidenceRefs []string) (task.MissionFeatureSet, task.MissionMilestoneSet) {
	now := task.Now()
	for idx := range features.Features {
		features.Features[idx].Status = status
		features.Features[idx].EvidenceRefs = uniqueRefs(append(features.Features[idx].EvidenceRefs, evidenceRefs...))
		if validationRef != "" {
			features.Features[idx].ValidatorFindingRefs = uniqueNonEmptyStrings(append(features.Features[idx].ValidatorFindingRefs, validationRef))
		}
		features.Features[idx].UpdatedAt = now
	}
	features.UpdatedAt = now
	for idx := range milestones.Milestones {
		if milestoneID != "" && milestones.Milestones[idx].MilestoneID != milestoneID {
			continue
		}
		milestones.Milestones[idx].Status = status
		milestones.Milestones[idx].EvidenceRefs = uniqueRefs(append(milestones.Milestones[idx].EvidenceRefs, evidenceRefs...))
		if validationRef != "" {
			milestones.Milestones[idx].ValidationRunRefs = uniqueNonEmptyStrings(append(milestones.Milestones[idx].ValidationRunRefs, validationRef))
		}
		milestones.Milestones[idx].UpdatedAt = now
	}
	milestones.UpdatedAt = now
	return features, milestones
}

func normalizeMissionStrings(values []string) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" {
			out = append(out, value)
		}
	}
	return uniqueNonEmptyStrings(out)
}

func missionTitleFromObjective(objective string) string {
	title := strings.TrimSpace(objective)
	if title == "" {
		return "Mission"
	}
	if len(title) > 80 {
		title = strings.TrimSpace(title[:80])
	}
	return title
}

func missionWorkspaceRef(missionID, rel string) string {
	return "workspace:" + filepath.ToSlash(filepath.Join(".ngen", "missions", strings.TrimSpace(missionID), filepath.FromSlash(strings.TrimSpace(rel))))
}

func missionCriteriaFromSlashPrompt(prompt missionSlashPrompt) []string {
	objective := strings.TrimSpace(prompt.Objective)
	if objective == "" {
		return nil
	}
	label := "mission"
	if prompt.IsGoal {
		label = "goal"
	}
	return []string{fmt.Sprintf("%s objective is satisfied with evidence: %s", label, missionTitleFromObjective(objective))}
}

func missionSlashRuntimeMessage(view task.MissionView, prompt missionSlashPrompt) string {
	if strings.TrimSpace(prompt.Objective) == "" {
		return fmt.Sprintf("Mission %s is open for root task %s. Type `/mission <prompt>` or `/goal <prompt>` to set it directly, then use `ngen mission approve %s --json` before `ngen mission run %s --json`.", view.Mission.MissionID, view.Mission.RootTaskID, view.Mission.MissionID, view.Mission.MissionID)
	}
	label := "mission"
	if prompt.IsGoal {
		label = "goal"
	}
	return fmt.Sprintf("Set %s %s for root task %s from the prompt. Review the contract, approve with `ngen mission approve %s --json`, then use `ngen mission run %s --json` for the mission loop.", label, view.Mission.MissionID, view.Mission.RootTaskID, view.Mission.MissionID, view.Mission.MissionID)
}

type missionSlashPrompt struct {
	Command   string
	Objective string
	IsGoal    bool
}

func parseMissionSlashPrompt(message string) (missionSlashPrompt, bool) {
	trimmed := strings.TrimSpace(message)
	if trimmed == "" || !strings.HasPrefix(trimmed, "/") {
		return missionSlashPrompt{}, false
	}
	command, rest, _ := strings.Cut(trimmed, " ")
	command = strings.TrimSpace(strings.TrimPrefix(command, "/"))
	command = strings.ToLower(command)
	intent := missionSlashPrompt{
		Command:   command,
		Objective: strings.TrimSpace(rest),
	}
	switch command {
	case "mission", "missions":
		return intent, true
	case "goal", "goals":
		intent.IsGoal = true
		return intent, true
	default:
		return missionSlashPrompt{}, false
	}
}

func isMissionSlashPrompt(message string) bool {
	_, ok := parseMissionSlashPrompt(message)
	return ok
}
