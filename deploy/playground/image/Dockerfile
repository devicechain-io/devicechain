# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0
#
# The playground driver image: dcctl, dc-simulator and the playground binary, in one
# static, non-root image. Nothing here is published; build it and push it to a registry
# you own, then name it in the root's driver_image (README.md, "Build the driver image").
#
# The binaries are not built here. dcctl and dc-simulator are the RELEASE binaries of the
# version the instance runs (a dcctl built from another version drives an instance it was
# not written for), so the build context is a directory holding the three linux/amd64
# binaries, put there by image/build.sh.
#
# The base is digest-pinned and bumped by hand, like the other images in this tree that
# ship nowhere.
FROM gcr.io/distroless/static-debian12:nonroot@sha256:afa5c872c891853ca7fcf1f12c3edb23f7eeef36189728842dd51042ff57f7ab

COPY dcctl dc-simulator playground /usr/local/bin/

# 65532 is the nonroot user. HOME is a volume in the Deployment (the sim records live
# under it), so nothing is written to the image's own filesystem.
USER 65532:65532
ENV HOME=/home/playground
ENTRYPOINT ["/usr/local/bin/playground"]
CMD ["run"]
