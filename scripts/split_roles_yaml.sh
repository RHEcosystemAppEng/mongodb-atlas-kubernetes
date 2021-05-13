#!/bin/bash

set -eou pipefail

# This is the script that allows to avoid the restrictions from the controller-gen tool that puts both Role and ClusterRole
# to the same role.yaml file (and kustomize doesn't provide an easy way to use only a single resource from file as a base)
# So we simply split the 'config/rbac/roles.yaml' file into two new files
# For Mac OS, gcsplit is used instead. It can be installed using the command below:
# brew install coreutils
CSPLIT_CMD=csplit
if [ `uname` = 'Darwin' ]; then
    CSPLIT_CMD=gcsplit
fi
gcsplit config/rbac/role.yaml '/---/' '{*}' &> /dev/null
rm xx00
mv xx01 config/rbac/clusterwide/role.yaml
mv xx02 config/rbac/namespaced/role.yaml
rm config/rbac/role.yaml