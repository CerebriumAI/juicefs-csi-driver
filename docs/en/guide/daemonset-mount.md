# DaemonSet Mount for StorageClass

This feature allows JuiceFS CSI Driver to deploy Mount Pods as DaemonSets instead of individual Pods when using StorageClass with mount sharing enabled. This provides better resource management and control over which nodes run Mount Pods.

## Overview

When `STORAGE_CLASS_SHARE_MOUNT` is enabled, JuiceFS CSI Driver shares Mount Pods across multiple PVCs that use the same StorageClass. By default, these are created as individual Pods. With the DaemonSet option, Mount Pods are deployed as DaemonSets, providing:

- **Better resource control**: DaemonSets ensure one Mount Pod per selected node
- **Node affinity support**: Control which nodes run Mount Pods using nodeAffinity
- **Automatic lifecycle management**: DaemonSets handle Pod creation/deletion automatically
- **Simplified operations**: Easier to manage and monitor Mount Pods
- **Works with existing StorageClasses**: No need to modify or recreate StorageClasses

## Configuration

### Enable DaemonSet Mount

To enable DaemonSet mount for StorageClass, set these environment variables in the CSI Driver deployment:

```yaml
env:
  - name: STORAGE_CLASS_SHARE_MOUNT
    value: "true"
  - name: STORAGE_CLASS_DAEMONSET
    value: "true"
```

### Configure Node Affinity

There are two ways to configure node affinity for DaemonSet Mount Pods:

#### Method 1: ConfigMap (Recommended for existing StorageClasses)

Create a ConfigMap to define node affinity for your StorageClasses without modifying them:

```yaml
apiVersion: v1
kind: ConfigMap
metadata:
  name: juicefs-daemonset-config
  namespace: kube-system
data:
  # Default configuration for all StorageClasses
  default: |
    nodeAffinity:
      requiredDuringSchedulingIgnoredDuringExecution:
        nodeSelectorTerms:
        - matchExpressions:
          - key: node-role.kubernetes.io/control-plane
            operator: DoesNotExist
  
  # Configuration for specific StorageClass by name
  my-existing-storageclass: |
    nodeAffinity:
      requiredDuringSchedulingIgnoredDuringExecution:
        nodeSelectorTerms:
        - matchExpressions:
          - key: juicefs/mount-node
            operator: In
            values:
            - "true"
```

This method works with existing StorageClasses without any modifications.

#### Method 2: StorageClass Parameters (For new StorageClasses)

For new StorageClasses, you can specify `nodeAffinity` directly in the parameters:

```yaml
apiVersion: storage.k8s.io/v1
kind: StorageClass
metadata:
  name: juicefs-sc-daemonset
provisioner: csi.juicefs.com
parameters:
  # ... other parameters ...
  
  # Node affinity configuration for DaemonSet Mount Pods
  nodeAffinity: |
    requiredDuringSchedulingIgnoredDuringExecution:
      nodeSelectorTerms:
      - matchExpressions:
        - key: juicefs/mount-node
          operator: In
          values:
          - "true"
```

## How It Works

1. When a PVC is created using a StorageClass with DaemonSet mount enabled:
   - The CSI Driver checks if a DaemonSet for this StorageClass already exists
   - If not, it looks for node affinity configuration:
     - First checks the ConfigMap for StorageClass-specific or default configuration
     - Falls back to StorageClass parameters if specified
   - Creates a new DaemonSet with the configured node affinity
   - If DaemonSet exists, it adds a reference to the existing DaemonSet

2. The DaemonSet ensures Mount Pods are running on selected nodes:
   - Pods are automatically created on nodes matching the affinity rules
   - Mount paths are shared across PVCs using the same StorageClass

3. When a PVC is deleted:
   - The reference is removed from the DaemonSet
   - If no references remain, the DaemonSet is deleted

## Priority Order

The system checks for node affinity configuration in this order:

1. **StorageClass parameters** (if `nodeAffinity` is specified)
2. **ConfigMap with StorageClass name** as key
3. **ConfigMap default** configuration
4. **No affinity** (DaemonSet runs on all nodes)

## Use Cases

### Dedicated Mount Nodes

Label specific nodes for running Mount Pods:

```bash
kubectl label nodes node1 node2 node3 juicefs/mount-node=true
```

Then use nodeAffinity in StorageClass to target these nodes.

### High-Performance Nodes

Prefer nodes with better resources for Mount Pods:

```yaml
nodeAffinity: |
  preferredDuringSchedulingIgnoredDuringExecution:
  - weight: 100
    preference:
      matchExpressions:
      - key: node.kubernetes.io/instance-type
        operator: In
        values:
        - m5.xlarge
        - m5.2xlarge
```

### Exclude Control Plane

Prevent Mount Pods from running on control plane nodes:

```yaml
nodeAffinity: |
  requiredDuringSchedulingIgnoredDuringExecution:
    nodeSelectorTerms:
    - matchExpressions:
      - key: node-role.kubernetes.io/control-plane
        operator: DoesNotExist
```

## Monitoring

You can monitor DaemonSet Mount Pods using standard Kubernetes commands:

```bash
# List all mount DaemonSets
kubectl get daemonset -n kube-system | grep juicefs

# Check DaemonSet status
kubectl describe daemonset juicefs-<uniqueid>-mount-ds -n kube-system

# List pods created by DaemonSet
kubectl get pods -n kube-system -l juicefs.com/mount-by=juicefs-csi-driver
```

## Limitations

- Node affinity is only applied when both `STORAGE_CLASS_SHARE_MOUNT` and `STORAGE_CLASS_DAEMONSET` are enabled
- All PVCs using the same StorageClass share the same DaemonSet and node affinity rules
- Changing node affinity requires recreating the StorageClass and associated PVCs

## Migration

To migrate from Pod-based mounts to DaemonSet mounts:

1. Enable the feature flags in CSI Driver
2. Create a new StorageClass with desired node affinity
3. Migrate PVCs to the new StorageClass
4. Old Mount Pods will be replaced by DaemonSet Pods

## Troubleshooting

### DaemonSet Pods not created

Check if nodes match the affinity rules:

```bash
kubectl get nodes --show-labels | grep <your-label>
```

### Mount Pods on unexpected nodes

Verify the nodeAffinity configuration:

```bash
kubectl get storageclass <name> -o yaml | grep -A 10 nodeAffinity
```

### References not cleaned up

Check DaemonSet annotations:

```bash
kubectl get daemonset <name> -n kube-system -o jsonpath='{.metadata.annotations}'
```
