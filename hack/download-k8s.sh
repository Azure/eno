#!/bin/bash

set -e

etcd_release="v3.6.0"
minor=$1
if [ ! -z "$minor" ]; then
    version=$(curl -sL https://dl.k8s.io/release/stable-1.${minor}.txt)
else
    version=$(curl -sL https://dl.k8s.io/release/stable.txt)
fi

function download_apiserver() {
    target=".k8s/${version}/kube-apiserver"
    if [ -f "$target" ]; then
        echo "kube-apiserver ${version} already exists, skipping download." > /dev/stderr
        return
    fi

    echo "downloading kube-apiserver ${version}..." > /dev/stderr
    curl -sL -o "$target" "https://dl.k8s.io/release/${version}/bin/linux/amd64/kube-apiserver"
    echo "finished downloading kube-apiserver ${version}..." > /dev/stderr
    chmod +x "$target"
}

function download_etcd() {
    if [ ! -f ".k8s/etcd-${etcd_release}-linux-amd64/etcd" ]; then
        echo "downloading etcd ${etcd_release}..." > /dev/stderr
        curl -sL "https://github.com/etcd-io/etcd/releases/download/${etcd_release}/etcd-${etcd_release}-linux-amd64.tar.gz" | tar -zx -C ".k8s"
    fi
}

dir=".k8s/${version}"
mkdir -p "$dir"
download_apiserver &
download_etcd &
wait
cp ".k8s/etcd-${etcd_release}-linux-amd64/etcd" "$dir"
echo "$(pwd)/$dir"
