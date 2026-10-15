// SPDX-License-Identifier: Apache-2.0

package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"sigs.k8s.io/controller-runtime/pkg/event"
)

func TestNamespaceLabelPredicate(t *testing.T) {
	for _, tc := range []struct {
		name, selector  string
		matching, other map[string]string
	}{
		{"equality", "watch=true", map[string]string{"watch": "true"}, map[string]string{"watch": "false"}},
		{"existence", "watch", map[string]string{"watch": "true"}, nil},
		{"absence", "!watch", nil, map[string]string{"watch": "true"}},
		{"inequality", "watch!=false", map[string]string{"watch": "true"}, map[string]string{"watch": "false"}},
		{"set", "watch in (true,yes)", map[string]string{"watch": "yes"}, map[string]string{"watch": "false"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			selector, err := labels.Parse(tc.selector)
			require.NoError(t, err)
			p := namespaceLabelPredicate(selector)
			matching := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Labels: tc.matching}}
			other := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Labels: tc.other}}
			assert.True(t, p.Update(event.UpdateEvent{ObjectOld: matching, ObjectNew: other}), "leaving selection")
			assert.True(t, p.Update(event.UpdateEvent{ObjectOld: other, ObjectNew: matching}), "entering selection")
			assert.True(t, p.Update(event.UpdateEvent{ObjectOld: matching, ObjectNew: matching}), "remaining selected")
			assert.False(t, p.Update(event.UpdateEvent{ObjectOld: other, ObjectNew: other}), "remaining unselected")
			assert.True(t, p.Create(event.CreateEvent{Object: matching}))
			assert.False(t, p.Create(event.CreateEvent{Object: other}))
			assert.True(t, p.Delete(event.DeleteEvent{Object: matching}))
			assert.False(t, p.Delete(event.DeleteEvent{Object: other}))
			assert.True(t, p.Generic(event.GenericEvent{Object: matching}))
			assert.False(t, p.Generic(event.GenericEvent{Object: other}))
		})
	}
}
