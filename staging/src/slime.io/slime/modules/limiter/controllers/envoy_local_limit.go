/*
* @Author: yangdihang
* @Date: 2020/11/19
 */

package controllers

import (
	"fmt"
	"hash/adler32"
	"strconv"
	"strings"

	envoy_core_v3 "github.com/envoyproxy/go-control-plane/envoy/config/core/v3"
	envoy_config_route_v3 "github.com/envoyproxy/go-control-plane/envoy/config/route/v3"
	envoy_ratelimit_v3 "github.com/envoyproxy/go-control-plane/envoy/extensions/common/ratelimit/v3"
	envoy_extensions_filters_http_local_ratelimit_v3 "github.com/envoyproxy/go-control-plane/envoy/extensions/filters/http/local_ratelimit/v3"
	envoy_match_v3 "github.com/envoyproxy/go-control-plane/envoy/type/matcher/v3"
	envoy_type_v3 "github.com/envoyproxy/go-control-plane/envoy/type/v3"
	structpb "github.com/gogo/protobuf/types"
	"github.com/golang/protobuf/ptypes/duration"
	"github.com/golang/protobuf/ptypes/wrappers"
	networking "istio.io/api/networking/v1alpha3"
	"k8s.io/apimachinery/pkg/types"
	"slime.io/slime/framework/util"
	microservicev1alpha2 "slime.io/slime/modules/limiter/api/v1alpha2"
	"slime.io/slime/modules/limiter/model"
)

// store the vhostName/routeName and action
// if routeName is "", it means the config is specified to vhost (contain all route in vhost)
type routeConfig struct {
	gw        bool
	action    []*envoy_config_route_v3.RateLimit_Action
	routeName string
	vhostName string
	direction string
}

// vhostName/routeName,
func genVhostRouteName(rc *routeConfig) string {
	return fmt.Sprintf("%s/%s", rc.vhostName, rc.routeName)
}

func getTarget(inner, outer *microservicev1alpha2.Target) *microservicev1alpha2.Target {
	target := &microservicev1alpha2.Target{}

	if inner != nil {
		target = inner
	} else {
		target = outer
	}
	return target
}

func generateRouteConfigs(inner, outer *microservicev1alpha2.Target, gw bool) []*routeConfig {
	rcs := make([]*routeConfig, 0)

	target := getTarget(inner, outer)
	// build outbound route
	if target != nil && target.Direction == model.Outbound {
		log.Debugf("build outbound route config base on %s", target.String())
		if len(target.Route) > 0 {
			for _, route := range target.Route {
				parts := strings.Split(route, "/")
				if len(parts) != 2 {
					continue
				}
				vhostName, routeName := parts[0], parts[1]
				if target.Port != 0 {
					vhostName = fmt.Sprintf("%s:%d", vhostName, target.Port)
				}
				rc := &routeConfig{
					routeName: routeName,
					vhostName: vhostName,
					direction: target.Direction,
					gw:        gw,
				}
				rcs = append(rcs, rc)
			}
			return rcs
		} else if len(target.Host) > 0 {
			for _, vhost := range target.Host {
				vhostName := vhost
				if target.Port != 0 {
					vhostName = fmt.Sprintf("%s:%d", vhostName, target.Port)
				}
				rc := &routeConfig{
					routeName: "",
					vhostName: vhostName,
					direction: target.Direction,
					gw:        gw,
				}
				rcs = append(rcs, rc)
			}
			return rcs
		}
	}
	// build inbound by default
	log.Debugf("build default inbound route config base on %s", target)
	rcs = generateDefaultInboundRouteConfigs(target)
	return rcs
}

// if port is zero, allow any
func generateDefaultInboundRouteConfigs(target *microservicev1alpha2.Target) []*routeConfig {
	rcs := make([]*routeConfig, 0)
	if target == nil || target.Port == 0 {
		rcs = append(rcs, &routeConfig{
			routeName: model.InboundDefaultRoute,
			vhostName: model.AllowAllPort,
			direction: model.Inbound,
		})
		return rcs
	}
	rcs = append(rcs, &routeConfig{
		routeName: model.InboundDefaultRoute,
		vhostName: fmt.Sprintf("%s|%s|%d", model.Inbound, "http", target.Port),
		direction: model.Inbound,
	})
	return rcs
}

func generateHttpRouterPatch(descriptors []*microservicev1alpha2.SmartLimitDescriptor, params *LimiterSpec) ([]*networking.EnvoyFilter_EnvoyConfigObjectPatch, error) {
	patches := make([]*networking.EnvoyFilter_EnvoyConfigObjectPatch, 0)
	// route2RouteConfig store the vhostName/routeName => routeConfig
	route2RouteConfig := make(map[string][]*routeConfig)
	routeNameList := make([]string, 0)

	for _, descriptor := range descriptors {
		rcs := generateRouteConfigs(descriptor.Target, params.target, params.gw)
		action := generateRouteRateLimitAction(descriptor, params.loc)
		if action == nil {
			continue
		}
		for _, rc := range rcs {
			rc.action = action
			vHostRouteName := genVhostRouteName(rc)

			if _, ok := route2RouteConfig[vHostRouteName]; !ok {
				route2RouteConfig[vHostRouteName] = []*routeConfig{rc}
				routeNameList = append(routeNameList, vHostRouteName)
			} else {
				route2RouteConfig[vHostRouteName] = append(route2RouteConfig[vHostRouteName], rc)
			}
		}
	}
	log.Debugf("get route2RouteConfig %v", route2RouteConfig)
	for _, rn := range routeNameList {
		rcs, ok := route2RouteConfig[rn]
		if !ok {
			continue
		}
		rateLimits := make([]*envoy_config_route_v3.RateLimit, 0)
		for _, rc := range rcs {
			rateLimits = append(rateLimits, &envoy_config_route_v3.RateLimit{Actions: rc.action})
		}

		match := generateEnvoyVhostMatch(rcs[0])
		if match.Context == networking.EnvoyFilter_GATEWAY {
			params.context = model.Gateway
		} else if match.Context == networking.EnvoyFilter_SIDECAR_OUTBOUND {
			params.context = model.Outbound
		}

		patch := &networking.EnvoyFilter_EnvoyConfigObjectPatch{
			Match: match,
			Patch: &networking.EnvoyFilter_Patch{
				Operation: networking.EnvoyFilter_Patch_MERGE,
			},
		}

		if rcs[0].routeName == "" {
			// route name not specified, patch to vhost
			vh := &envoy_config_route_v3.VirtualHost{RateLimits: rateLimits}
			vhStruct, err := util.MessageToStruct(vh)
			if err != nil {
				return nil, err
			}
			patch.ApplyTo = networking.EnvoyFilter_VIRTUAL_HOST
			patch.Patch.Value = vhStruct
		} else {
			route := &envoy_config_route_v3.Route{
				Action: &envoy_config_route_v3.Route_Route{
					Route: &envoy_config_route_v3.RouteAction{
						RateLimits: rateLimits,
					},
				},
			}
			routeStruct, err := util.MessageToStruct(route)
			if err != nil {
				return nil, err
			}
			patch.ApplyTo = networking.EnvoyFilter_HTTP_ROUTE
			patch.Patch.Value = routeStruct
		}
		patches = append(patches, patch)
	}
	return patches, nil
}

// only enable local rate limit
func generateHttpFilterLocalRateLimitPatch(context string) *networking.EnvoyFilter_EnvoyConfigObjectPatch {
	localRateLimit := &envoy_extensions_filters_http_local_ratelimit_v3.LocalRateLimit{
		StatPrefix: util.StructEnvoyLocalRateLimitLimiter,
	}
	local, err := util.MessageToStruct(localRateLimit)
	if err != nil {
		log.Errorf("can not be here, convert message to struct err,%+v", err.Error())
		return nil
	}

	patch := &networking.EnvoyFilter_EnvoyConfigObjectPatch{
		ApplyTo: networking.EnvoyFilter_HTTP_FILTER,
		Match:   generateEnvoyHttpFilterMatch(context),
		Patch: &networking.EnvoyFilter_Patch{
			Operation: networking.EnvoyFilter_Patch_INSERT_BEFORE,
			Value: &structpb.Struct{
				Fields: map[string]*structpb.Value{
					util.StructHttpFilterName: {
						Kind: &structpb.Value_StringValue{StringValue: util.EnvoyLocalRateLimit},
					},
					util.StructHttpFilterTypedConfig: {
						Kind: &structpb.Value_StructValue{
							StructValue: &structpb.Struct{
								Fields: map[string]*structpb.Value{
									util.StructAnyAtType: {
										Kind: &structpb.Value_StringValue{StringValue: util.TypeURLUDPATypedStruct},
									},
									util.StructAnyTypeURL: {
										Kind: &structpb.Value_StringValue{StringValue: util.TypeURLEnvoyLocalRateLimit},
									},
									util.StructAnyValue: {
										Kind: &structpb.Value_StructValue{StructValue: local},
									},
								},
							},
						},
					},
				},
			},
		},
	}
	return patch
}

func generateLocalRateLimitPerFilterPatch(descriptors []*microservicev1alpha2.SmartLimitDescriptor, params *LimiterSpec) []*networking.EnvoyFilter_EnvoyConfigObjectPatch {
	patches := make([]*networking.EnvoyFilter_EnvoyConfigObjectPatch, 0)
	route2Descriptors := make(map[string][]*microservicev1alpha2.SmartLimitDescriptor)
	route2RouteConfig := make(map[string][]*routeConfig)
	routeNameList := make([]string, 0)

	for _, descriptor := range descriptors {
		rcs := generateRouteConfigs(descriptor.Target, params.target, params.gw)
		for _, rc := range rcs {
			vHostRouteName := genVhostRouteName(rc)

			if _, ok := route2Descriptors[vHostRouteName]; !ok {
				route2Descriptors[vHostRouteName] = []*microservicev1alpha2.SmartLimitDescriptor{descriptor}
				routeNameList = append(routeNameList, vHostRouteName)
			} else {
				route2Descriptors[vHostRouteName] = append(route2Descriptors[vHostRouteName], descriptor)
			}

			if _, ok := route2RouteConfig[vHostRouteName]; !ok {
				route2RouteConfig[vHostRouteName] = []*routeConfig{rc}
			} else {
				route2RouteConfig[vHostRouteName] = append(route2RouteConfig[vHostRouteName], rc)
			}
		}
	}

	for _, vr := range routeNameList {
		desc, ok := route2Descriptors[vr]
		if !ok {
			continue
		}
		rcs, ok := route2RouteConfig[vr]
		if !ok {
			continue
		}

		// build token bucket
		localRateLimitDescriptors := generateLocalRateLimitDescriptors(desc, params.loc)
		localRateLimit := &envoy_extensions_filters_http_local_ratelimit_v3.LocalRateLimit{
			TokenBucket:    generateCustomTokenBucket(100000, 100000, 1),
			Descriptors:    localRateLimitDescriptors,
			StatPrefix:     util.StructEnvoyLocalRateLimitLimiter,
			FilterEnabled:  generateEnvoyLocalRateLimitEnabled(),
			FilterEnforced: generateEnvoyLocalRateLimitEnforced(),
		}
		local, err := util.MessageToStruct(localRateLimit)
		if err != nil {
			return nil
		}

		patch := &networking.EnvoyFilter_EnvoyConfigObjectPatch{
			Match: generateEnvoyVhostMatch(rcs[0]),
			Patch: generatePerFilterPatch(local),
		}
		if rcs[0].routeName == "" {
			patch.ApplyTo = networking.EnvoyFilter_VIRTUAL_HOST
		} else {
			patch.ApplyTo = networking.EnvoyFilter_HTTP_ROUTE
		}
		patches = append(patches, patch)
	}
	return patches
}

/*
// if key/value is not empty, envoyplugin is needed, we will not generate http route patch
// 有match时，只有当header中的值与match相匹配才会进行对路由进行action限流，需要注意的是RegexMatch(name 的值是否匹配正则)与
// PresentMatch(name是否存在)互斥
// 这里之前打算在pb声明为oneof,但是用kubebuilder生成api的过程中无法识别相关interface{}
*/
func generateRouteRateLimitAction(descriptor *microservicev1alpha2.SmartLimitDescriptor, loc types.NamespacedName) []*envoy_config_route_v3.RateLimit_Action {
	action := &envoy_config_route_v3.RateLimit_Action{}
	if descriptor.CustomKey != "" && descriptor.CustomValue != "" {
		log.Infof("customKey/customValue is not empty, users should apply a envoyplugin with same customKey/customValue")
		return nil
	} else if len(descriptor.Match) == 0 {
		action.ActionSpecifier = &envoy_config_route_v3.RateLimit_Action_GenericKey_{
			GenericKey: &envoy_config_route_v3.RateLimit_Action_GenericKey{
				DescriptorValue: generateDescriptorValue(descriptor, loc),
			},
		}
	} else {
		headers := make([]*envoy_config_route_v3.HeaderMatcher, 0)
		for _, match := range descriptor.Match {
			header := &envoy_config_route_v3.HeaderMatcher{}
			header.Name = match.Name
			header.InvertMatch = generateInvertMatch(match)
			switch {
			case match.RegexMatch != "":
				header.HeaderMatchSpecifier = generateSafeRegexMatch(match)
			case match.ExactMatch != "":
				header.HeaderMatchSpecifier = generateExactMatch(match)
			case match.PrefixMatch != "":
				header.HeaderMatchSpecifier = generatePrefixMatch(match)
			case match.SuffixMatch != "":
				header.HeaderMatchSpecifier = generateSuffixMatch(match)
			default:
				if match.IsExactMatchEmpty {
					header.HeaderMatchSpecifier = generateExactMatch(match)
				} else if match.PresentMatch {
					header.HeaderMatchSpecifier = generatePresentMatch(match)
				} else if match.PresentMatchSeparate {
					action.ActionSpecifier = &envoy_config_route_v3.RateLimit_Action_RequestHeaders_{
						RequestHeaders: &envoy_config_route_v3.RateLimit_Action_RequestHeaders{
							HeaderName:    match.Name,
							DescriptorKey: generateDescriptorKey(descriptor, loc),
						},
					}
					generic := &envoy_config_route_v3.RateLimit_Action{}
					generic.ActionSpecifier = &envoy_config_route_v3.RateLimit_Action_GenericKey_{
						GenericKey: &envoy_config_route_v3.RateLimit_Action_GenericKey{
							DescriptorValue: generateDescriptorValue(descriptor, loc),
						},
					}
					return []*envoy_config_route_v3.RateLimit_Action{generic, action}
				}
			}
			headers = append(headers, header)
		}

		action.ActionSpecifier = &envoy_config_route_v3.RateLimit_Action_HeaderValueMatch_{
			HeaderValueMatch: &envoy_config_route_v3.RateLimit_Action_HeaderValueMatch{
				DescriptorValue: generateDescriptorValue(descriptor, loc),
				Headers:         headers,
			},
		}
	}
	return []*envoy_config_route_v3.RateLimit_Action{action}
}

func generateLocalRateLimitDescriptors(descriptors []*microservicev1alpha2.SmartLimitDescriptor, loc types.NamespacedName) []*envoy_ratelimit_v3.LocalRateLimitDescriptor {
	localRateLimitDescriptors := make([]*envoy_ratelimit_v3.LocalRateLimitDescriptor, 0)
	for _, item := range descriptors {
		entries := generateLocalRateLimitDescriptorEntries(item, loc)
		tokenBucket := generateTokenBucket(item)
		localRateLimitDescriptors = append(localRateLimitDescriptors, &envoy_ratelimit_v3.LocalRateLimitDescriptor{
			Entries:     entries,
			TokenBucket: tokenBucket,
		})
	}
	return localRateLimitDescriptors
}

func generateLocalRateLimitDescriptorEntries(item *microservicev1alpha2.SmartLimitDescriptor, loc types.NamespacedName) []*envoy_ratelimit_v3.RateLimitDescriptor_Entry {
	entry := &envoy_ratelimit_v3.RateLimitDescriptor_Entry{}
	if item.CustomKey != "" && item.CustomValue != "" {
		entry.Key = item.CustomKey
		entry.Value = item.CustomValue
	} else if len(item.Match) == 0 {
		entry.Key = model.GenericKey
		entry.Value = generateDescriptorValue(item, loc)
	} else {
		entry.Key = model.HeaderValueMatch
		entry.Value = generateDescriptorValue(item, loc)
	}
	return []*envoy_ratelimit_v3.RateLimitDescriptor_Entry{entry}
}

func generateTokenBucket(item *microservicev1alpha2.SmartLimitDescriptor) *envoy_type_v3.TokenBucket {
	i, _ := strconv.Atoi(item.Action.Quota)
	return &envoy_type_v3.TokenBucket{
		MaxTokens: uint32(i),
		FillInterval: &duration.Duration{
			Seconds: item.Action.FillInterval.Seconds,
			Nanos:   item.Action.FillInterval.Nanos,
		},
		TokensPerFill: &wrappers.UInt32Value{Value: uint32(i)},
	}
}

func generateDescriptorValue(item *microservicev1alpha2.SmartLimitDescriptor, loc types.NamespacedName) string {
	id := adler32.Checksum([]byte(item.String() + loc.String()))
	return fmt.Sprintf("Service[%s.%s]-Id[%d]", loc.Name, loc.Namespace, id)
}

func generateSafeRegexMatch(match *microservicev1alpha2.SmartLimitDescriptor_HeaderMatcher) *envoy_config_route_v3.HeaderMatcher_SafeRegexMatch {
	return &envoy_config_route_v3.HeaderMatcher_SafeRegexMatch{
		SafeRegexMatch: &envoy_match_v3.RegexMatcher{
			EngineType: &envoy_match_v3.RegexMatcher_GoogleRe2{
				GoogleRe2: &envoy_match_v3.RegexMatcher_GoogleRE2{},
			},
			Regex: match.RegexMatch,
		},
	}
}

func generatePrefixMatch(match *microservicev1alpha2.SmartLimitDescriptor_HeaderMatcher) *envoy_config_route_v3.HeaderMatcher_PrefixMatch {
	return &envoy_config_route_v3.HeaderMatcher_PrefixMatch{PrefixMatch: match.PrefixMatch}
}

func generateSuffixMatch(match *microservicev1alpha2.SmartLimitDescriptor_HeaderMatcher) *envoy_config_route_v3.HeaderMatcher_SuffixMatch {
	return &envoy_config_route_v3.HeaderMatcher_SuffixMatch{SuffixMatch: match.SuffixMatch}
}

func generateExactMatch(match *microservicev1alpha2.SmartLimitDescriptor_HeaderMatcher) *envoy_config_route_v3.HeaderMatcher_ExactMatch {
	return &envoy_config_route_v3.HeaderMatcher_ExactMatch{ExactMatch: match.ExactMatch}
}

func generateInvertMatch(match *microservicev1alpha2.SmartLimitDescriptor_HeaderMatcher) bool {
	return match.InvertMatch
}

func generatePresentMatch(match *microservicev1alpha2.SmartLimitDescriptor_HeaderMatcher) *envoy_config_route_v3.HeaderMatcher_PresentMatch {
	return &envoy_config_route_v3.HeaderMatcher_PresentMatch{PresentMatch: match.PresentMatch}
}

// TODO
func generateCustomTokenBucket(maxTokens, tokensPerFill, second int) *envoy_type_v3.TokenBucket {
	return &envoy_type_v3.TokenBucket{
		MaxTokens: uint32(maxTokens),
		FillInterval: &duration.Duration{
			Seconds: int64(second),
		},
		TokensPerFill: &wrappers.UInt32Value{Value: uint32(tokensPerFill)},
	}
}

// % of requests that will check the local rate limit decision, but not enforce,
// for a given route_key specified in the local rate limit configuration. Defaults to 0.
func generateEnvoyLocalRateLimitEnabled() *envoy_core_v3.RuntimeFractionalPercent {
	return &envoy_core_v3.RuntimeFractionalPercent{
		RuntimeKey: util.StructEnvoyLocalRateLimitEnabled,
		DefaultValue: &envoy_type_v3.FractionalPercent{
			Numerator:   100,
			Denominator: envoy_type_v3.FractionalPercent_HUNDRED,
		},
	}
}

//  % of requests that will enforce the local rate limit decision for a given route_key specified in the local rate limit configuration.
// Defaults to 0. This can be used to test what would happen before fully enforcing the outcome.
func generateEnvoyLocalRateLimitEnforced() *envoy_core_v3.RuntimeFractionalPercent {
	return &envoy_core_v3.RuntimeFractionalPercent{
		RuntimeKey: util.StructEnvoyLocalRateLimitEnforced,
		DefaultValue: &envoy_type_v3.FractionalPercent{
			Numerator:   100,
			Denominator: envoy_type_v3.FractionalPercent_HUNDRED,
		},
	}
}

func generateEnvoyVhostMatch(rc *routeConfig) *networking.EnvoyFilter_EnvoyConfigObjectMatch {
	// default context is inbound
	match := &networking.EnvoyFilter_EnvoyConfigObjectMatch{
		Context: networking.EnvoyFilter_SIDECAR_INBOUND,
		ObjectTypes: &networking.EnvoyFilter_EnvoyConfigObjectMatch_RouteConfiguration{
			RouteConfiguration: &networking.EnvoyFilter_RouteConfigurationMatch{
				Vhost: &networking.EnvoyFilter_RouteConfigurationMatch_VirtualHostMatch{
					Route: &networking.EnvoyFilter_RouteConfigurationMatch_RouteMatch{
						Name: rc.routeName,
					},
				},
			},
		},
	}
	// if gateway is enabled, match context should be EnvoyFilter_GATEWAY
	if rc.gw {
		match.Context = networking.EnvoyFilter_GATEWAY
		log.Debugf("gw is true, set context to gateway")
	} else if rc.direction == model.Outbound {
		match.Context = networking.EnvoyFilter_SIDECAR_OUTBOUND
		log.Debugf("direction is outbound and gw is false, set context to outbound")
	}
	// if allow_any, config.RouteConfiguration.Vhost.Name is ""
	config, ok := match.ObjectTypes.(*networking.EnvoyFilter_EnvoyConfigObjectMatch_RouteConfiguration)
	if !ok {
		log.Errorf("covert to EnvoyFilter_EnvoyConfigObjectMatch_RouteConfiguration err, can not be here")
		return match
	}

	if rc.routeName == "" {
		config.RouteConfiguration.Vhost.Route = nil
	}
	config.RouteConfiguration.Vhost.Name = rc.vhostName

	return match
}

func generatePerFilterPatch(local *structpb.Struct) *networking.EnvoyFilter_Patch {
	return &networking.EnvoyFilter_Patch{
		Operation: networking.EnvoyFilter_Patch_MERGE,
		Value: &structpb.Struct{
			Fields: map[string]*structpb.Value{
				model.TypePerFilterConfig: {
					Kind: &structpb.Value_StructValue{
						StructValue: &structpb.Struct{
							Fields: map[string]*structpb.Value{
								util.EnvoyLocalRateLimit: {
									Kind: &structpb.Value_StructValue{
										StructValue: &structpb.Struct{
											Fields: map[string]*structpb.Value{
												util.StructAnyAtType: {
													Kind: &structpb.Value_StringValue{StringValue: util.TypeURLUDPATypedStruct},
												},
												util.StructAnyTypeURL: {
													Kind: &structpb.Value_StringValue{StringValue: util.TypeURLEnvoyLocalRateLimit},
												},
												util.StructAnyValue: {
													Kind: &structpb.Value_StructValue{StructValue: local},
												},
											},
										},
									},
								},
							},
						},
					},
				},
			},
		},
	}
}
