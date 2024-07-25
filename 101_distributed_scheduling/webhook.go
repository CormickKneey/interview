package main

import (
	"context"
	"encoding/json"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"net/http"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"
)

/*
	Let's add the webhook in an operator-sdk based project, import it by:
	mgr.GetWebhookServer().Register("/webhooks/policy-injector", &webhook.Admission{Handler: NewPolicyInjector(...)})
*/

func NewPolicyInjector(
	topologyKey, appLabelKey string,
	preferences []string,
	decoder *admission.Decoder,
) *PolicyInjector {
	return &PolicyInjector{
		topologyKey: topologyKey,
		preferences: preferences,
		appLabelKey: appLabelKey,
		decoder:     decoder,
	}
}

// PolicyInjector implements a mutating webhook for Deployment resources.
type PolicyInjector struct {
	// init by: admission.NewDecoder(mgr.GetScheme())
	decoder *admission.Decoder

	// topologyKey is the target topology key.
	topologyKey string
	// preferences is the list of preference values to schedule first in different topology fields.
	preferences []string

	// appLabelKey is the well-known label in the deployment in the cluster to identify the app.
	appLabelKey string
}

// Handle handles the admission request for Deployment resources.
func (wh *PolicyInjector) Handle(ctx context.Context, req admission.Request) admission.Response {
	deployment := &appsv1.Deployment{}

	err := wh.decoder.Decode(req, deployment)
	if err != nil {
		return admission.Errored(http.StatusBadRequest, err)
	}

	if wh.mutateDeploymentWithAffinity(deployment) || wh.mutateDeploymentWithTopologySpreadConstraints(deployment) {
		marshaledDeployment, err := json.Marshal(deployment)
		if err != nil {
			return admission.Errored(http.StatusInternalServerError, err)
		}

		return admission.PatchResponseFromRaw(req.Object.Raw, marshaledDeployment)
	}
	return admission.Allowed("no change after injection")
}

// mutateDeploymentWithAffinity injects the desired affinity into the Deployment. Returns true if the deployment changed after injection.
func (wh *PolicyInjector) mutateDeploymentWithAffinity(deployment *appsv1.Deployment) bool {
	if len(wh.preferences) == 0 {
		return false
	}

	preferredSchedulingTerm := corev1.PreferredSchedulingTerm{
		Weight: 1,
		Preference: corev1.NodeSelectorTerm{
			MatchExpressions: []corev1.NodeSelectorRequirement{
				{
					Key:      wh.topologyKey,
					Operator: corev1.NodeSelectorOperator("In"),
					Values:   wh.preferences,
				},
			},
		},
	}

	if deployment.Spec.Template.Spec.Affinity == nil {
		deployment.Spec.Template.Spec.Affinity = &corev1.Affinity{}
	}

	if deployment.Spec.Template.Spec.Affinity.NodeAffinity == nil {
		deployment.Spec.Template.Spec.Affinity.NodeAffinity = &corev1.NodeAffinity{}
	}

	if len(deployment.Spec.Template.Spec.Affinity.NodeAffinity.PreferredDuringSchedulingIgnoredDuringExecution) == 0 {
		deployment.Spec.Template.Spec.Affinity.NodeAffinity.PreferredDuringSchedulingIgnoredDuringExecution = []corev1.PreferredSchedulingTerm{
			preferredSchedulingTerm,
		}
		return true
	}

	// only check whether it has the same key.
	for i, term := range deployment.Spec.Template.Spec.Affinity.NodeAffinity.PreferredDuringSchedulingIgnoredDuringExecution {
		for _, preference := range term.Preference.MatchExpressions {
			if preference.Key == wh.topologyKey {
				deployment.Spec.Template.Spec.Affinity.NodeAffinity.PreferredDuringSchedulingIgnoredDuringExecution[i] = preferredSchedulingTerm
				return false
			}
		}
	}

	// append the new term
	deployment.Spec.Template.Spec.Affinity.NodeAffinity.PreferredDuringSchedulingIgnoredDuringExecution = append(deployment.Spec.Template.Spec.Affinity.NodeAffinity.PreferredDuringSchedulingIgnoredDuringExecution, preferredSchedulingTerm)
	return true
}

// mutateDeploymentWithTopologySpreadConstraints injects the desired topologySpreadConstraints into the Deployment. Returns true if the deployment changed after injection.
func (wh *PolicyInjector) mutateDeploymentWithTopologySpreadConstraints(deployment *appsv1.Deployment) bool {
	topologySpreadConstraints := corev1.TopologySpreadConstraint{
		MaxSkew:           1,
		TopologyKey:       wh.topologyKey,
		WhenUnsatisfiable: corev1.DoNotSchedule,
		LabelSelector: &metav1.LabelSelector{
			MatchLabels: map[string]string{
				wh.appLabelKey: deployment.Labels[wh.appLabelKey],
			},
		},
		MatchLabelKeys: []string{appsv1.DefaultDeploymentUniqueLabelKey},
	}

	if len(deployment.Spec.Template.Spec.TopologySpreadConstraints) == 0 {
		deployment.Spec.Template.Spec.TopologySpreadConstraints = []corev1.TopologySpreadConstraint{topologySpreadConstraints}
		return true
	}

	// check whether it has the same key.
	for _, constraint := range deployment.Spec.Template.Spec.TopologySpreadConstraints {
		if constraint.TopologyKey == wh.topologyKey {
			return false
		}
	}
	deployment.Spec.Template.Spec.TopologySpreadConstraints = append(deployment.Spec.Template.Spec.TopologySpreadConstraints, topologySpreadConstraints)
	return true
}
