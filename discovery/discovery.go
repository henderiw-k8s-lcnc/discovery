package discovery

import (
	"fmt"
	"strings"
)

type ServiceDiscoveryType string

const (
	ServiceDiscoveryTypeNone   ServiceDiscoveryType = "none"
	ServiceDiscoveryTypeConsul ServiceDiscoveryType = "consul"
	ServiceDiscoveryTypeK8s    ServiceDiscoveryType = "k8s"
)

const (
	PodServiceTag = "pod"
)

func GetServiceName(prefix, name string) string {
	return strings.Join([]string{prefix, name}, "-")
}

func GetPodServiceTag(namespace, name string) string {
	return fmt.Sprintf("%s=%s.%s", PodServiceTag, namespace, name)
}

// GetPodValue return pod namspacedName if the key exists;
// if not an empty string is returned
func GetPodValue(tags map[string]string) string {
	return GetValue(PodServiceTag, tags)
}

// GetTagValue return pod namspacedName if the key exists;
// if not an empty string is returned
func GetValue(key string, tags map[string]string) string {
	v, ok := tags[key]
	if !ok {
		return ""
	}
	return v
}
