/*
Copyright 2021 Juicedata Inc

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package mount

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	k8sMount "k8s.io/utils/mount"

	jfsConfig "github.com/juicedata/juicefs-csi-driver/pkg/config"
	"github.com/juicedata/juicefs-csi-driver/pkg/k8sclient"
)

func TestMountSelector_SelectMount(t *testing.T) {
	ctx := context.Background()
	jfsConfig.Namespace = "test-ns"
	
	tests := []struct {
		name             string
		jfsSetting       *jfsConfig.JfsSetting
		byProcess        bool
		configMap        *corev1.ConfigMap
		globalShareMount bool
		globalDaemonSet  bool
		wantMountType    string // "process", "daemonset", "pod"
	}{
		{
			name: "process mount when ByProcess is true",
			jfsSetting: &jfsConfig.JfsSetting{
				UniqueId: "test-id",
			},
			byProcess:     true,
			wantMountType: "process",
		},
		{
			name: "daemonset mount from explicit mode",
			jfsSetting: &jfsConfig.JfsSetting{
				UniqueId:  "test-id",
				MountMode: string(jfsConfig.MountModeDaemonSet),
			},
			byProcess:     false,
			wantMountType: "daemonset",
		},
		{
			name: "shared pod mount from explicit mode",
			jfsSetting: &jfsConfig.JfsSetting{
				UniqueId:  "test-id",
				MountMode: string(jfsConfig.MountModeSharedPod),
			},
			byProcess:     false,
			wantMountType: "pod",
		},
		{
			name: "per-pvc pod mount from explicit mode",
			jfsSetting: &jfsConfig.JfsSetting{
				VolumeId:  "test-volume",
				MountMode: string(jfsConfig.MountModePVC),
			},
			byProcess:     false,
			wantMountType: "pod",
		},
		{
			name: "fallback to global daemonset when no mode specified",
			jfsSetting: &jfsConfig.JfsSetting{
				UniqueId: "test-id",
				PV: &corev1.PersistentVolume{
					Spec: corev1.PersistentVolumeSpec{
						StorageClassName: "test-sc",
					},
				},
			},
			byProcess:        false,
			globalShareMount: true,
			globalDaemonSet:  true,
			wantMountType:    "daemonset",
		},
		{
			name: "fallback to global shared pod when no mode specified",
			jfsSetting: &jfsConfig.JfsSetting{
				UniqueId: "test-id",
				PV: &corev1.PersistentVolume{
					Spec: corev1.PersistentVolumeSpec{
						StorageClassName: "test-sc",
					},
				},
			},
			byProcess:        false,
			globalShareMount: true,
			globalDaemonSet:  false,
			wantMountType:    "pod",
		},
		{
			name: "fallback to per-pvc when no configuration",
			jfsSetting: &jfsConfig.JfsSetting{
				VolumeId: "test-volume",
				PV: &corev1.PersistentVolume{
					Spec: corev1.PersistentVolumeSpec{
						StorageClassName: "test-sc",
					},
				},
			},
			byProcess:        false,
			globalShareMount: false,
			globalDaemonSet:  false,
			wantMountType:    "pod",
		},
		{
			name: "load from configmap - daemonset mode",
			jfsSetting: &jfsConfig.JfsSetting{
				UniqueId: "test-id",
				PV: &corev1.PersistentVolume{
					Spec: corev1.PersistentVolumeSpec{
						StorageClassName: "test-sc",
					},
				},
			},
			configMap: &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{
					Name:      jfsConfig.MountConfigMapName,
					Namespace: jfsConfig.Namespace,
				},
				Data: map[string]string{
					"test-sc": `mode: daemonset`,
				},
			},
			byProcess:     false,
			wantMountType: "daemonset",
		},
		{
			name: "load from configmap default - shared-pod mode",
			jfsSetting: &jfsConfig.JfsSetting{
				UniqueId: "test-id",
				PV: &corev1.PersistentVolume{
					Spec: corev1.PersistentVolumeSpec{
						StorageClassName: "unknown-sc",
					},
				},
			},
			configMap: &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{
					Name:      jfsConfig.MountConfigMapName,
					Namespace: jfsConfig.Namespace,
				},
				Data: map[string]string{
					jfsConfig.DefaultConfigKey: `mode: shared-pod`,
				},
			},
			byProcess:     false,
			wantMountType: "pod",
		},
	}
	
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Set global variables
			jfsConfig.ByProcess = tt.byProcess
			jfsConfig.StorageClassShareMount = tt.globalShareMount
			jfsConfig.StorageClassDaemonSet = tt.globalDaemonSet
			
			// Create fake k8s client
			var objects []runtime.Object
			if tt.configMap != nil {
				objects = append(objects, tt.configMap)
			}
			
			fakeClient := fake.NewSimpleClientset(objects...)
			k8sClient := &k8sclient.K8sClient{}
			k8sClient.Interface = fakeClient
			
			// Create MountSelector
			mounter := &k8sMount.FakeMounter{}
			m := NewMountSelector(k8sClient, k8sMount.SafeFormatAndMount{
				Interface: mounter,
				Exec:      nil,
			})
			
			// Select mount
			selector := m.(*MountSelector)
			mnt := selector.selectMount(ctx, tt.jfsSetting)
			
			// Check mount type
			switch tt.wantMountType {
			case "process":
				if _, ok := mnt.(*ProcessMount); !ok {
					t.Errorf("Expected ProcessMount, got %T", mnt)
				}
			case "daemonset":
				if _, ok := mnt.(*DaemonSetMount); !ok {
					t.Errorf("Expected DaemonSetMount, got %T", mnt)
				}
			case "pod":
				if _, ok := mnt.(*PodMount); !ok {
					t.Errorf("Expected PodMount, got %T", mnt)
				}
			}
		})
	}
}

func TestMountSelector_Fallback(t *testing.T) {
	ctx := context.Background()
	jfsConfig.Namespace = "test-ns"
	
	tests := []struct {
		name             string
		jfsSetting       *jfsConfig.JfsSetting
		configMap        *corev1.ConfigMap
		globalShareMount bool
		globalDaemonSet  bool
		wantMountMode    jfsConfig.MountMode
	}{
		{
			name: "no configmap, use global per-pvc default",
			jfsSetting: &jfsConfig.JfsSetting{
				VolumeId: "test-volume",
				PV: &corev1.PersistentVolume{
					Spec: corev1.PersistentVolumeSpec{
						StorageClassName: "test-sc",
					},
				},
			},
			configMap:        nil,
			globalShareMount: false,
			globalDaemonSet:  false,
			wantMountMode:    jfsConfig.MountModePVC,
		},
		{
			name: "no configmap, use global shared-pod",
			jfsSetting: &jfsConfig.JfsSetting{
				UniqueId: "test-id",
				PV: &corev1.PersistentVolume{
					Spec: corev1.PersistentVolumeSpec{
						StorageClassName: "test-sc",
					},
				},
			},
			configMap:        nil,
			globalShareMount: true,
			globalDaemonSet:  false,
			wantMountMode:    jfsConfig.MountModeSharedPod,
		},
		{
			name: "no configmap, use global daemonset",
			jfsSetting: &jfsConfig.JfsSetting{
				UniqueId: "test-id",
				PV: &corev1.PersistentVolume{
					Spec: corev1.PersistentVolumeSpec{
						StorageClassName: "test-sc",
					},
				},
			},
			configMap:        nil,
			globalShareMount: true,
			globalDaemonSet:  true,
			wantMountMode:    jfsConfig.MountModeDaemonSet,
		},
		{
			name: "invalid config in configmap, fallback to global",
			jfsSetting: &jfsConfig.JfsSetting{
				UniqueId: "test-id",
				PV: &corev1.PersistentVolume{
					Spec: corev1.PersistentVolumeSpec{
						StorageClassName: "test-sc",
					},
				},
			},
			configMap: &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{
					Name:      jfsConfig.MountConfigMapName,
					Namespace: jfsConfig.Namespace,
				},
				Data: map[string]string{
					"test-sc": `mode: invalid-mode`,
				},
			},
			globalShareMount: true,
			globalDaemonSet:  false,
			wantMountMode:    jfsConfig.MountModeSharedPod,
		},
		{
			name: "empty config in configmap, fallback to global",
			jfsSetting: &jfsConfig.JfsSetting{
				UniqueId: "test-id",
				PV: &corev1.PersistentVolume{
					Spec: corev1.PersistentVolumeSpec{
						StorageClassName: "test-sc",
					},
				},
			},
			configMap: &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{
					Name:      jfsConfig.MountConfigMapName,
					Namespace: jfsConfig.Namespace,
				},
				Data: map[string]string{
					"test-sc": ``,
				},
			},
			globalShareMount: false,
			globalDaemonSet:  false,
			wantMountMode:    jfsConfig.MountModePVC,
		},
		{
			name: "configmap overrides global settings",
			jfsSetting: &jfsConfig.JfsSetting{
				VolumeId: "test-volume",
				PV: &corev1.PersistentVolume{
					Spec: corev1.PersistentVolumeSpec{
						StorageClassName: "test-sc",
					},
				},
			},
			configMap: &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{
					Name:      jfsConfig.MountConfigMapName,
					Namespace: jfsConfig.Namespace,
				},
				Data: map[string]string{
					"test-sc": `mode: per-pvc`,
				},
			},
			globalShareMount: true,
			globalDaemonSet:  true,
			wantMountMode:    jfsConfig.MountModePVC,
		},
		{
			name: "use default key when storage class not found",
			jfsSetting: &jfsConfig.JfsSetting{
				UniqueId: "test-id",
				PV: &corev1.PersistentVolume{
					Spec: corev1.PersistentVolumeSpec{
						StorageClassName: "unknown-sc",
					},
				},
			},
			configMap: &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{
					Name:      jfsConfig.MountConfigMapName,
					Namespace: jfsConfig.Namespace,
				},
				Data: map[string]string{
					jfsConfig.DefaultConfigKey: `mode: shared-pod`,
					"other-sc": `mode: daemonset`,
				},
			},
			globalShareMount: false,
			globalDaemonSet:  false,
			wantMountMode:    jfsConfig.MountModeSharedPod,
		},
	}
	
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Set global variables
			jfsConfig.ByProcess = false
			jfsConfig.StorageClassShareMount = tt.globalShareMount
			jfsConfig.StorageClassDaemonSet = tt.globalDaemonSet
			
			// Create fake k8s client
			var objects []runtime.Object
			if tt.configMap != nil {
				objects = append(objects, tt.configMap)
			}
			
			fakeClient := fake.NewSimpleClientset(objects...)
			k8sClient := &k8sclient.K8sClient{}
			k8sClient.Interface = fakeClient
			
			// Load mount config
			err := jfsConfig.LoadMountConfig(ctx, k8sClient, tt.jfsSetting)
			if err != nil {
				t.Errorf("LoadMountConfig() error = %v", err)
				return
			}
			
			// Check mount mode
			actualMode := jfsConfig.MountMode(tt.jfsSetting.MountMode)
			if actualMode == "" {
				// Determine from helper functions
				if jfsConfig.ShouldUseDaemonSet(tt.jfsSetting) {
					actualMode = jfsConfig.MountModeDaemonSet
				} else if jfsConfig.ShouldUseSharedPod(tt.jfsSetting) {
					actualMode = jfsConfig.MountModeSharedPod
				} else {
					actualMode = jfsConfig.MountModePVC
				}
			}
			
			if actualMode != tt.wantMountMode {
				t.Errorf("Mount mode = %v, want %v", actualMode, tt.wantMountMode)
			}
		})
	}
}