// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"context"
	"fmt"

	dcv1beta1 "github.com/devicechain-io/dc-k8s/api/v1beta1"
	"github.com/fatih/color"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/dynamic"
)

// applyUpgradeValuesOverlay settles the overlay an upgrade renders with.
//
//   - --values replaces whatever the declaration records;
//   - --reset-values clears it;
//   - neither carries the recorded overlay forward unchanged.
//
// 🔴 A CARRIED OVERLAY IS RE-JUDGED AGAINST THIS RELEASE. It was valid when it was
// written; the chart this dcctl embeds may since have renamed a key or tightened a type,
// or the allowlist may have changed. Applying it blind would turn a version bump into a
// failed install ten minutes in; refusing here, with the key named and the two ways out,
// costs nothing.
func applyUpgradeValuesOverlay(st *State, inst *dcv1beta1.Instance, opts UpgradeOptions) error {
	recorded, err := decodeValuesOverlay(inst.Annotations)
	if err != nil {
		return err
	}
	st.ValuesOverlayRecorded = recorded
	switch {
	case opts.ResetValues:
		st.ValuesOverlay = nil
	case opts.ValuesOverlay != nil:
		st.ValuesOverlay = opts.ValuesOverlay
	default:
		st.ValuesOverlay = recorded
		if len(recorded) > 0 {
			if err := ValidateValuesOverlay(recorded); err != nil {
				return fmt.Errorf("the values overlay recorded on instance %q is not accepted by this release: %w\n"+
					"Nothing has been changed. Replace it with `--values <file>` or remove it with `--reset-values`",
					st.Instance, err)
			}
		}
	}
	return nil
}

// valuesOverlayChanges is whether this run changes the recorded overlay.
func valuesOverlayChanges(st *State) bool {
	return len(diffOverlays(st.ValuesOverlayRecorded, st.ValuesOverlay)) > 0
}

// sayValuesOverlay reports the overlay in effect and, under --dry-run, exactly what the
// run would change in the recorded one.
func sayValuesOverlay(st *State) {
	lines := diffOverlays(st.ValuesOverlayRecorded, st.ValuesOverlay)
	switch {
	case len(st.ValuesOverlay) == 0 && len(lines) == 0:
		return
	case len(st.ValuesOverlay) == 0:
		fmt.Printf("  %s %s\n", color.WhiteString("Overlay:"),
			color.YellowString("cleared (--reset-values); the instance returns to what dcctl computes"))
	case len(lines) == 0:
		fmt.Printf("  %s %s\n", color.WhiteString("Overlay:"),
			color.GreenString(fmt.Sprintf("%d tuned value(s), carried unchanged", len(overlayLeaves(st.ValuesOverlay)))))
	default:
		fmt.Printf("  %s %s\n", color.WhiteString("Overlay:"), color.GreenString("changes to the recorded tuning:"))
	}
	for _, l := range lines {
		fmt.Printf("    %s\n", l)
	}
	if st.DryRun && valuesOverlayChanges(st) {
		wouldDo("record the values overlay in the instance declaration")
	}
}

// precheckValuesOverlay renders the chart with the overlay in place, before anything is
// written, so a combination the chart refuses at render time (a request above its limit,
// two pods on a one-pod volume) stops the run while it still costs nothing.
func precheckValuesOverlay(ctx context.Context, st *State) error {
	if len(st.ValuesOverlay) == 0 {
		return nil
	}
	ch, err := loadEmbeddedChart()
	if err != nil {
		return fmt.Errorf("loading the chart to check the values overlay: %w", err)
	}
	if _, err := renderChartClientSide(ctx, ch, renderOnlyAuthoring(st)); err != nil {
		return fmt.Errorf("the chart refuses the values overlay in combination with this instance, "+
			"so nothing has been changed: %w", err)
	}
	return nil
}

// recordValuesOverlay writes the run's overlay onto the declaration (or removes the
// annotation when it is empty). A compare-and-swap, like every declaration write.
func recordValuesOverlay(ctx context.Context, dyn dynamic.Interface, instance string, overlay map[string]interface{}) error {
	inst, err := readInstanceCR(ctx, dyn, instance)
	if err != nil {
		return err
	}
	if inst == nil {
		return fmt.Errorf("the declaration for instance %q disappeared before the values overlay could be recorded; "+
			"re-run to start from a clean read", instance)
	}
	if len(overlay) == 0 {
		if _, had := inst.Annotations[AnnotationValuesOverlay]; !had {
			return nil
		}
		delete(inst.Annotations, AnnotationValuesOverlay)
	} else {
		enc, err := encodeValuesOverlay(overlay)
		if err != nil {
			return err
		}
		if inst.Annotations == nil {
			inst.Annotations = map[string]string{}
		}
		inst.Annotations[AnnotationValuesOverlay] = enc
	}
	obj, err := instanceToUnstructured(inst)
	if err != nil {
		return err
	}
	if _, err := dyn.Resource(instanceGVR).Update(ctx, obj, metav1.UpdateOptions{}); err != nil {
		if apierrors.IsConflict(err) {
			return fmt.Errorf("the declaration for instance %q changed while the values overlay was being recorded; "+
				"something else is editing it -- re-run once you know what", instance)
		}
		return fmt.Errorf("recording the values overlay on instance %q: %w", instance, err)
	}
	return nil
}

// recordValuesOverlayFor is the bootstrap path's entry: it builds its own client, and is a
// seam so the step that calls it can be tested without a cluster.
var recordValuesOverlayFor = func(ctx context.Context, kubeContext, instance string, overlay map[string]interface{}) error {
	dyn, _, _, err := kubeClients(kubeContext)
	if err != nil {
		return fmt.Errorf("connecting to the cluster to record the values overlay: %w", err)
	}
	return recordValuesOverlay(ctx, dyn, instance, overlay)
}

// adoptRecordedValuesOverlay is the bootstrap side of carrying: a bootstrap that is given no
// --values over a declaration that already records an overlay (a restore re-run, or the
// finish of a bootstrap that never completed) keeps that overlay rather than rendering
// without it. It also records what the declaration holds, for the diff.
func adoptRecordedValuesOverlay(st *State, existing *dcv1beta1.Instance) error {
	if existing == nil {
		return nil
	}
	recorded, err := decodeValuesOverlay(existing.Annotations)
	if err != nil {
		return err
	}
	st.ValuesOverlayRecorded = recorded
	if st.ValuesOverlay == nil && len(recorded) > 0 {
		if err := ValidateValuesOverlay(recorded); err != nil {
			return fmt.Errorf("the values overlay recorded on instance %q is not accepted by this release: %w\n"+
				"Nothing has been changed", st.Instance, err)
		}
		st.ValuesOverlay = recorded
	}
	return nil
}
