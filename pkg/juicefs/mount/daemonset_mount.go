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
	"fmt"
	"strings"
	"time"
	
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/uuid"
	"k8s.io/client-go/util/retry"
	"k8s.io/klog/v2"
	k8sMount "k8s.io/utils/mount"

	"github.com/juicedata/juicefs-csi-driver/pkg/common"
	jfsConfig "github.com/juicedata/juicefs-csi-driver/pkg/config"
	"github.com/juicedata/juicefs-csi-driver/pkg/juicefs/mount/builder"
	"github.com/juicedata/juicefs-csi-driver/pkg/k8sclient"
	"github.com/juicedata/juicefs-csi-driver/pkg/util"
	"github.com/juicedata/juicefs-csi-driver/pkg/util/resource"
)

type DaemonSetMount struct {
	log klog.Logger
	k8sMount.SafeFormatAndMount
	K8sClient *k8sclient.K8sClient
}

var _ MntInterface = &DaemonSetMount{}

func NewDaemonSetMount(client *k8sclient.K8sClient, mounter k8sMount.SafeFormatAndMount) MntInterface {
	return &DaemonSetMount{
		klog.NewKlogr().WithName("daemonset-mount"),
		mounter, client}
}

func (d *DaemonSetMount) JMount(ctx context.Context, appInfo *jfsConfig.AppInfo, jfsSetting *jfsConfig.JfsSetting) error {
	d.log = util.GenLog(ctx, d.log, "JMount")
	hashVal := jfsConfig.GenHashOfSetting(d.log, *jfsSetting)
	jfsSetting.HashVal = hashVal
	jfsSetting.UpgradeUUID = string(uuid.NewUUID())
	
	var dsName string
	var err error

	if err = func() error {
		lock := jfsConfig.GetPodLock(hashVal)
		lock.Lock()
		defer lock.Unlock()

		dsName = d.genDaemonSetName(jfsSetting)
		
		// Create or update DaemonSet
		err = d.createOrUpdateDaemonSet(ctx, dsName, jfsSetting)
		if err != nil {
			return err
		}
		
		return nil
	}(); err != nil {
		return err
	}

	// Wait for DaemonSet to be ready on the current node
	err = d.waitUntilDaemonSetReady(ctx, dsName, jfsSetting)
	if err != nil {
		return err
	}

	if jfsSetting.UUID != "" {
		// Set uuid as annotation in DaemonSet for clean cache
		err = d.setUUIDAnnotation(ctx, dsName, jfsSetting.UUID)
		if err != nil {
			return err
		}
	}
	
	return nil
}

func (d *DaemonSetMount) GetMountRef(ctx context.Context, target, dsName string) (int, error) {
	log := util.GenLog(ctx, d.log, "GetMountRef")
	
	// For DaemonSet, we track references differently
	// Each PV using this DaemonSet will add an annotation
	ds, err := d.K8sClient.GetDaemonSet(ctx, dsName, jfsConfig.Namespace)
	if err != nil {
		if k8serrors.IsNotFound(err) {
			return 0, nil
		}
		log.Error(err, "Get DaemonSet error", "dsName", dsName)
		return 0, err
	}
	
	// Count references in annotations
	refCount := 0
	referencePrefix := "juicefs-"
	for k := range ds.Annotations {
		if strings.HasPrefix(k, referencePrefix) {
			refCount++
		}
	}
	
	return refCount, nil
}

func (d *DaemonSetMount) UmountTarget(ctx context.Context, target, dsName string) error {
	log := util.GenLog(ctx, d.log, "UmountTarget")
	
	// Unmount the target
	log.Info("lazy umount", "target", target)
	if err := util.UmountPath(ctx, target, true); err != nil {
		return err
	}

	// Remove reference from DaemonSet
	key := util.GetReferenceKey(target)
	err := d.removeReference(ctx, dsName, key)
	if err != nil {
		log.Error(err, "Remove reference from DaemonSet error", "dsName", dsName)
		return err
	}
	
	// Check if DaemonSet has any remaining references
	refCount, err := d.GetMountRef(ctx, target, dsName)
	if err != nil {
		return err
	}
	
	// If no more references, we can delete the DaemonSet
	if refCount == 0 {
		log.Info("No more references, deleting DaemonSet", "dsName", dsName)
		if err := d.K8sClient.DeleteDaemonSet(ctx, dsName, jfsConfig.Namespace); err != nil && !k8serrors.IsNotFound(err) {
			log.Error(err, "Delete DaemonSet error", "dsName", dsName)
			return err
		}
	}
	
	return nil
}

func (d *DaemonSetMount) JUmount(ctx context.Context, target, podName string) error {
	// For DaemonSet mount, podName might be the DaemonSet name or we need to find it
	dsName := podName
	if dsName == "" {
		dsName = d.getDaemonSetNameFromTarget(ctx, target)
		if dsName == "" {
			return fmt.Errorf("cannot find DaemonSet for target %s", target)
		}
	}
	return d.UmountTarget(ctx, target, dsName)
}

func (d *DaemonSetMount) JCreateVolume(ctx context.Context, jfsSetting *jfsConfig.JfsSetting) error {
	// Volume creation is not supported via DaemonSet
	return fmt.Errorf("volume creation not supported via DaemonSet mount")
}

func (d *DaemonSetMount) JDeleteVolume(ctx context.Context, jfsSetting *jfsConfig.JfsSetting) error {
	// Volume deletion is not supported via DaemonSet
	return fmt.Errorf("volume deletion not supported via DaemonSet mount")
}

func (d *DaemonSetMount) AddRefOfMount(ctx context.Context, target string, podName string) error {
	// For DaemonSet, we add reference as annotation
	dsName := podName
	if dsName == "" {
		dsName = d.getDaemonSetNameFromTarget(ctx, target)
		if dsName == "" {
			return fmt.Errorf("cannot find DaemonSet for target %s", target)
		}
	}
	key := util.GetReferenceKey(target)
	return d.addReference(ctx, dsName, key, target)
}

func (d *DaemonSetMount) CleanCache(ctx context.Context, image string, id string, volumeId string, cacheDirs []string) error {
	// Cache cleaning implementation
	// This would need to be implemented based on your cache cleaning strategy
	log := util.GenLog(ctx, d.log, "CleanCache")
	log.Info("Cache cleaning requested", "volumeId", volumeId)
	// For now, return nil as cache cleaning might be handled differently for DaemonSets
	return nil
}

func (d *DaemonSetMount) genDaemonSetName(jfsSetting *jfsConfig.JfsSetting) string {
	return builder.GenDaemonSetNameByUniqueId(jfsSetting.UniqueId)
}

func (d *DaemonSetMount) createOrUpdateDaemonSet(ctx context.Context, dsName string, jfsSetting *jfsConfig.JfsSetting) error {
	log := util.GenLog(ctx, d.log, "createOrUpdateDaemonSet")
	
	// Load DaemonSet configuration from ConfigMap if not already set
	if err := jfsConfig.LoadDaemonSetNodeAffinity(ctx, d.K8sClient, jfsSetting); err != nil {
		log.Error(err, "Failed to load DaemonSet node affinity, proceeding without it")
	}
	
	r := builder.NewDaemonSetBuilder(jfsSetting, 0)
	secret := r.NewSecret()
	builder.SetPVAsOwner(&secret, jfsSetting.PV)
	key := util.GetReferenceKey(jfsSetting.TargetPath)
	
	// Check if DaemonSet exists
	existingDS, err := d.K8sClient.GetDaemonSet(ctx, dsName, jfsConfig.Namespace)
	if err != nil && !k8serrors.IsNotFound(err) {
		log.Error(err, "Get DaemonSet error", "dsName", dsName)
		return err
	}
	
	// Create or update secret
	if err := resource.CreateOrUpdateSecret(ctx, d.K8sClient, &secret); err != nil {
		return err
	}
	
	if k8serrors.IsNotFound(err) {
		// DaemonSet doesn't exist, create it
		log.Info("Creating new DaemonSet", "dsName", dsName)
		newDS, err := r.NewMountDaemonSet(dsName)
		if err != nil {
			log.Error(err, "Generate DaemonSet error", "dsName", dsName)
			return err
		}
		
		// Add reference annotation
		newDS.Annotations[key] = jfsSetting.TargetPath
		
		if _, err := d.K8sClient.CreateDaemonSet(ctx, newDS); err != nil {
			log.Error(err, "Create DaemonSet error", "dsName", dsName)
			return err
		}
	} else {
		// DaemonSet exists, add reference
		log.Info("DaemonSet exists, adding reference", "dsName", dsName)
		
		// Check if hash matches
		if existingDS.Labels[common.PodJuiceHashLabelKey] != jfsSetting.HashVal {
			log.Info("Hash mismatch, updating DaemonSet", "dsName", dsName, 
				"oldHash", existingDS.Labels[common.PodJuiceHashLabelKey],
				"newHash", jfsSetting.HashVal)
			
			// Update DaemonSet with new configuration
			newDS, err := r.NewMountDaemonSet(dsName)
			if err != nil {
				return err
			}
			
			// Preserve existing annotations
			referencePrefix := "juicefs-"
			for k, v := range existingDS.Annotations {
				if strings.HasPrefix(k, referencePrefix) {
					newDS.Annotations[k] = v
				}
			}
			// Add new reference
			newDS.Annotations[key] = jfsSetting.TargetPath
			
			// Update DaemonSet
			existingDS.Spec = newDS.Spec
			existingDS.Labels = newDS.Labels
			existingDS.Annotations = newDS.Annotations
			
			if err := d.K8sClient.UpdateDaemonSet(ctx, existingDS); err != nil {
				log.Error(err, "Update DaemonSet error", "dsName", dsName)
				return err
			}
		} else {
			// Just add reference
			if err := d.addReference(ctx, dsName, key, jfsSetting.TargetPath); err != nil {
				return err
			}
		}
	}
	
	return nil
}

func (d *DaemonSetMount) waitUntilDaemonSetReady(ctx context.Context, dsName string, jfsSetting *jfsConfig.JfsSetting) error {
	log := util.GenLog(ctx, d.log, "waitUntilDaemonSetReady")
	
	// Wait for DaemonSet to have pods ready on current node
	timeout := 5 * time.Minute
	waitCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	
	for {
		select {
		case <-waitCtx.Done():
			return fmt.Errorf("timeout waiting for DaemonSet %s to be ready on node %s", dsName, jfsConfig.NodeName)
		default:
			ds, err := d.K8sClient.GetDaemonSet(waitCtx, dsName, jfsConfig.Namespace)
			if err != nil {
				log.Error(err, "Get DaemonSet error", "dsName", dsName)
				time.Sleep(2 * time.Second)
				continue
			}
			
			// Check if DaemonSet has pods scheduled on current node
			labelSelector := &metav1.LabelSelector{
				MatchLabels: map[string]string{
					common.PodTypeKey:          common.PodTypeValue,
					common.PodUniqueIdLabelKey: jfsSetting.UniqueId,
				},
			}
			
			pods, err := d.K8sClient.ListPod(waitCtx, jfsConfig.Namespace, labelSelector, nil)
			if err != nil {
				log.Error(err, "List pods error")
				time.Sleep(2 * time.Second)
				continue
			}
			
			// Find pod on current node
			for _, pod := range pods {
				if pod.Spec.NodeName == jfsConfig.NodeName {
					// Check if pod is ready
					if resource.IsPodReady(&pod) {
						log.Info("DaemonSet pod is ready on node", "podName", pod.Name, "nodeName", jfsConfig.NodeName)
						
						// Update mount path from the pod
						mountPath, _, err := util.GetMountPathOfPod(pod)
						if err != nil {
							log.Error(err, "Get mount path from pod error", "podName", pod.Name)
							return err
						}
						jfsSetting.MountPath = mountPath
						return nil
					}
				}
			}
			
			log.V(1).Info("Waiting for DaemonSet pod to be ready", "dsName", dsName, "desiredNumberScheduled", ds.Status.DesiredNumberScheduled, "numberReady", ds.Status.NumberReady)
			time.Sleep(2 * time.Second)
		}
	}
}

func (d *DaemonSetMount) addReference(ctx context.Context, dsName, key, value string) error {
	return retry.RetryOnConflict(retry.DefaultBackoff, func() error {
		ds, err := d.K8sClient.GetDaemonSet(ctx, dsName, jfsConfig.Namespace)
		if err != nil {
			return err
		}
		
		if ds.Annotations == nil {
			ds.Annotations = make(map[string]string)
		}
		ds.Annotations[key] = value
		
		return d.K8sClient.UpdateDaemonSet(ctx, ds)
	})
}

func (d *DaemonSetMount) removeReference(ctx context.Context, dsName, key string) error {
	return retry.RetryOnConflict(retry.DefaultBackoff, func() error {
		ds, err := d.K8sClient.GetDaemonSet(ctx, dsName, jfsConfig.Namespace)
		if err != nil {
			if k8serrors.IsNotFound(err) {
				return nil
			}
			return err
		}
		
		if ds.Annotations != nil {
			delete(ds.Annotations, key)
		}
		
		return d.K8sClient.UpdateDaemonSet(ctx, ds)
	})
}

func (d *DaemonSetMount) setUUIDAnnotation(ctx context.Context, dsName, uuid string) error {
	return retry.RetryOnConflict(retry.DefaultBackoff, func() error {
		ds, err := d.K8sClient.GetDaemonSet(ctx, dsName, jfsConfig.Namespace)
		if err != nil {
			return err
		}
		
		if ds.Annotations == nil {
			ds.Annotations = make(map[string]string)
		}
		ds.Annotations[common.JuiceFSUUID] = uuid
		
		return d.K8sClient.UpdateDaemonSet(ctx, ds)
	})
}

func (d *DaemonSetMount) getDaemonSetNameFromTarget(ctx context.Context, target string) string {
	// List all DaemonSets and find the one with this target
	dsList, err := d.K8sClient.ListDaemonSet(ctx, jfsConfig.Namespace, nil)
	if err != nil {
		return ""
	}
	
	key := util.GetReferenceKey(target)
	for _, ds := range dsList {
		if ds.Annotations != nil && ds.Annotations[key] == target {
			return ds.Name
		}
	}
	
	return ""
}