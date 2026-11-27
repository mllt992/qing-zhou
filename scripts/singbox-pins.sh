#!/usr/bin/env bash
# Shared immutable inputs for the release core and protocol regressions.
SB_TAG=v1.14.5
SB_COMMIT=a6844a780ba173e189a018d0ac62df67cd50b8e9
# Official 1.14.5 already requires this sing-vmess (mux concurrent writes,
# packetaddr byte counts, and the xudp frame read fix). It still contains the
# earlier Vision fixes (a0c58542bd9d, 9b95ab8c9478).
VMESS_VERSION=v0.2.9-0.20261124075649-4acc92a6f6c6
VMESS_COMMIT=4acc92a6f6c60e4fad6d73311403a682d70d8b80
VMESS_SUM='h1:vyyQcx1p4dVam3oHPbxIUp/bzP1vKmHaidQ42iuRMa4='
VMESS_MOD_SUM='h1:P11scgTxMxVVQ8dlM27yNm3Cro40mD0+gHbnqrNGDuY='
CORE_VERSION=1.14.5+qz-trojan.b85be9da
# Historical 1.14.2 pin, used only by transport-buffer negative controls. Official
# 1.14.3 already contains the selected 07512b10 three-file backport, so the
# release build no longer applies that patch.
TRANSPORT_BASELINE_TAG=v1.14.2
TRANSPORT_BASELINE_COMMIT=af6e64c3b69e6132ebaee0e1a3d24e93903f6709
TRANSPORT_UPSTREAM_COMMIT=07512b1093aa38626fca00dbba07bc5d77375861
TRANSPORT_PATCH_SHA256=731a661d2121470feb6e04c64d0c6a2b6185819dac9c11fb50af06f2c87e9a44
# Project maintenance patch (not an upstream fix), see docs/CORE_TROJAN_HANDSHAKE_FIX.md.
# transport/trojan/service.go is unchanged from 1.14.2 through 1.14.5.
TROJAN_PATCH_SHA256=b85be9dae59431e59e431cdff004629ea43c869983f136e6fccf64cec50d48a7
