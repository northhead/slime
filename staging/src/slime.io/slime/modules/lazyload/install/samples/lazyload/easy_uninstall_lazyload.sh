#!/bin/bash
if [[ "$#" -eq 0 ]]; then
  echo "No specified tag or commit. Use the latest tag."
  tag_or_commit=$(curl -s https://api.github.com/repos/slime-io/slime/tags | grep 'name' | cut -d\" -f4 | head -1)
  if [[ -z $tag_or_commit ]]; then
    echo "Failed to get the latest tag. Exited."
    exit 1
  fi
  echo "The Latest slime tag: $tag_or_commit."
else
  tag_or_commit=$1
  echo "Use specified tag or commit: $tag_or_commit"
fi

crds_url="https://raw.githubusercontent.com/slime-io/slime/$tag_or_commit/install/init/crds-v1.yaml"
deployment_slimeboot_url="https://raw.githubusercontent.com/slime-io/slime/$tag_or_commit/install/init/deployment_slime-boot.yaml"
slimeboot_lazyload_url="https://raw.githubusercontent.com/slime-io/slime/$slime_tag_or_commit/staging/src/slime.io/slime/modules/lazyload/install/samples/lazyload/slimeboot_cluster_accesslog.yaml"

for i in $(kubectl get ns --no-headers |awk '{print $1}');do kubectl delete servicefence -n $i --all;done
kubectl delete -f "${slimeboot_lazyload_url}"
kubectl delete -f "${deployment_slimeboot_url}"
kubectl delete -f "${crds_url}"
kubectl delete ns mesh-operator
