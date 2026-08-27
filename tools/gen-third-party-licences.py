#!/usr/bin/env python3
"""Regenerates third-party-licences.md for the panaudia-server binary.

Go modules come from `go list -deps` of the server (the linux/amd64
`xsmm` build graph, the widest), each with its LICENSE text verbatim
from the module cache. The C libraries, the vendored FFT and the
embedded HRTF set are the hand-written sections at the bottom of this
script. Run from the repo root after a dependency change:

    tools/gen-third-party-licences.py > third-party-licences.md
"""
import glob, json, os, subprocess, sys

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))

def modules():
    out = subprocess.run(
        ["go", "list", "-deps", "-json", "-tags", "xsmm,nolibopusfile", "."],
        cwd=os.path.join(ROOT, "server"),
        env={**os.environ, "GOWORK": "off", "GOOS": "linux", "GOARCH": "amd64"},
        capture_output=True, text=True, check=True).stdout
    dec, i, mods = json.JSONDecoder(), 0, {}
    while i < len(out):
        while i < len(out) and out[i].isspace():
            i += 1
        if i >= len(out):
            break
        obj, i = dec.raw_decode(out, i)
        m = obj.get("Module")
        if not m or m.get("Main"):
            continue
        r = m.get("Replace")
        mods[m["Path"]] = dict(version=m.get("Version", ""), dir=(r or m)["Dir"],
                              replace=(r["Path"] + " " + r.get("Version", "")) if r else "")
    return mods

def licence_text(d):
    for pat in ("LICENSE*", "LICENCE*", "COPYING*"):
        hits = sorted(glob.glob(os.path.join(d, pat)))
        if hits:
            with open(hits[0], errors="replace") as f:
                return os.path.basename(hits[0]), f.read().strip()
    return None, None

def kind(text):
    t = text or ""
    if "Apache License" in t: return "Apache-2.0"
    if "Mozilla Public License" in t: return "MPL-2.0"
    if "ISC License" in t or "ISC" == t[:3]: return "ISC"
    if "Permission is hereby granted, free of charge" in t: return "MIT"
    if "Redistribution and use in source and binary forms" in t:
        return "BSD-3-Clause" if "endorse or promote" in t else "BSD-2-Clause"
    return "see text"

# Modules whose pinned version predates a LICENSE file that upstream has
# since added. Text is the upstream file verbatim; the note says why it
# is not in the module cache.
KNOWN = {
    # (empty since 2026-08-27: qlog is pinned past its LICENSE commit)
}

GO_AUTHORS = "golang.org/x/"
APACHE_NOTE = ("Apache-2.0. The full licence text is reproduced once in the "
               "Apache License section at the end of this file.")

def main():
    mods = modules()
    w = sys.stdout.write
    w("# panaudia-server Third Party Licences\n\n")
    w("panaudia-server (the binary and the container image built from\n"
      "this repository) includes the following third-party software and\n"
      "data. Copyright notices and licence terms are reproduced below.\n"
      "The Go module sections are generated from the build graph by\n"
      "`tools/gen-third-party-licences.py`; the remaining sections are\n"
      "maintained by hand. If you are aware of any errors or omissions\n"
      "please let us know.\n\n")

    w("## Go modules\n\n")
    x_mods, apache_text = [], None
    for path in sorted(mods):
        m = mods[path]
        if path.startswith(GO_AUTHORS):
            x_mods.append((path, m["version"]))
            continue
        if path == "github.com/panaudia/panaudia-lasa/engine":
            continue  # this repository; see LICENSE at the root
        fname, text = licence_text(m["dir"])
        w("-" * 73 + "\n")
        w(f"### {path}\n")
        w(f"https://{path}\n\n")
        w(f"{m['version']}" + (f" (built from {m['replace']})" if m["replace"] else "") + "\n\n")
        if text is None and path in KNOWN:
            k, text, note = KNOWN[path]
            w(f"{k}\n\n{note}\n\n{text}\n\n")
            continue
        if text is None:
            sys.stderr.write(f"error: no LICENSE for {path} in {m['dir']}\n")
            sys.exit(1)
        k = kind(text)
        if k == "Apache-2.0":
            apache_text = apache_text or text
            w(APACHE_NOTE + "\n\n")
            continue
        w(f"{k}\n\n{text}\n\n")

    w("-" * 73 + "\n")
    w("### golang.org/x/* and the Go standard library\n")
    w("https://go.dev\n\n")
    w("BSD-3-Clause (The Go Authors). Modules: " +
      ", ".join(f"{p} {v}" for p, v in x_mods) +
      ", plus the Go runtime and standard library the binary is compiled with.\n\n")
    _, text = licence_text(mods[x_mods[0][0]]["dir"])
    w(text + "\n\n")

    w(HAND_WRITTEN)

    if apache_text:
        w("-" * 73 + "\n### Apache License\n\n" + apache_text + "\n")

HAND_WRITTEN = r"""## C libraries linked into the binary

-------------------------------------------------------------------------
### opus-codec
https://opus-codec.org

BSD-3-Clause. Linked dynamically (libopus) through gopkg.in/hraban/opus.v2
and the engine's own multistream bindings (engine/inout/multistream.go).

https://opus-codec.org/license/

Both the reference implementation and the revised implementations on
opus-codec.org are available under the three-clause BSD license.

Copyright 2001-2011 Xiph.Org, Skype Limited, Octasic,
                    Jean-Marc Valin, Timothy B. Terriberry,
                    CSIRO, Gregory Maxwell, Mark Borgerding,
                    Erik de Castro Lopo

Redistribution and use in source and binary forms, with or without
modification, are permitted provided that the following conditions
are met:

- Redistributions of source code must retain the above copyright
notice, this list of conditions and the following disclaimer.

- Redistributions in binary form must reproduce the above copyright
notice, this list of conditions and the following disclaimer in the
documentation and/or other materials provided with the distribution.

- Neither the name of Internet Society, IETF or IETF Trust, nor the
names of specific contributors, may be used to endorse or promote
products derived from this software without specific prior written
permission.

THIS SOFTWARE IS PROVIDED BY THE COPYRIGHT HOLDERS AND CONTRIBUTORS
``AS IS'' AND ANY EXPRESS OR IMPLIED WARRANTIES, INCLUDING, BUT NOT
LIMITED TO, THE IMPLIED WARRANTIES OF MERCHANTABILITY AND FITNESS FOR
A PARTICULAR PURPOSE ARE DISCLAIMED. IN NO EVENT SHALL THE COPYRIGHT
OWNER OR CONTRIBUTORS BE LIABLE FOR ANY DIRECT, INDIRECT, INCIDENTAL,
SPECIAL, EXEMPLARY, OR CONSEQUENTIAL DAMAGES (INCLUDING, BUT NOT
LIMITED TO, PROCUREMENT OF SUBSTITUTE GOODS OR SERVICES; LOSS OF USE,
DATA, OR PROFITS; OR BUSINESS INTERRUPTION) HOWEVER CAUSED AND ON ANY
THEORY OF LIABILITY, WHETHER IN CONTRACT, STRICT LIABILITY, OR TORT
(INCLUDING NEGLIGENCE OR OTHERWISE) ARISING IN ANY WAY OUT OF THE USE
OF THIS SOFTWARE, EVEN IF ADVISED OF THE POSSIBILITY OF SUCH DAMAGE.

Patents: Opus is covered by patents made available under royalty-free
licences by Xiph.Org, Broadcom and Microsoft, granted automatically to
everyone; see https://opus-codec.org/license/ for the full patent
licence texts and the IPR disclosure discussion.

-------------------------------------------------------------------------
### libxsmm
https://github.com/libxsmm/libxsmm

BSD-3-Clause. Statically linked into the linux/amd64 build (`-tags xsmm`)
as the GEMM backend, at the commit pinned in docker/Dockerfile.

https://github.com/libxsmm/libxsmm/blob/main/LICENSE.md

Copyright (c) 2009-2023, Intel Corporation
Copyright (c) 2017-2022, Friedrich Schiller University Jena
Copyright (c) 2012-2021, Technische Universitaet Muenchen
Copyright (c) 2016-2020, Google Inc.
All rights reserved.

Redistribution and use in source and binary forms, with or without
modification, are permitted provided that the following conditions are met:

* Redistributions of source code must retain the above copyright notice, this
  list of conditions and the following disclaimer.

* Redistributions in binary form must reproduce the above copyright notice,
  this list of conditions and the following disclaimer in the documentation
  and/or other materials provided with the distribution.

* Neither the name of the copyright holder nor the names of its
  contributors may be used to endorse or promote products derived from
  this software without specific prior written permission.

THIS SOFTWARE IS PROVIDED BY THE COPYRIGHT HOLDERS AND CONTRIBUTORS "AS IS"
AND ANY EXPRESS OR IMPLIED WARRANTIES, INCLUDING, BUT NOT LIMITED TO, THE
IMPLIED WARRANTIES OF MERCHANTABILITY AND FITNESS FOR A PARTICULAR PURPOSE ARE
DISCLAIMED. IN NO EVENT SHALL THE COPYRIGHT HOLDER OR CONTRIBUTORS BE LIABLE
FOR ANY DIRECT, INDIRECT, INCIDENTAL, SPECIAL, EXEMPLARY, OR CONSEQUENTIAL
DAMAGES (INCLUDING, BUT NOT LIMITED TO, PROCUREMENT OF SUBSTITUTE GOODS OR
SERVICES; LOSS OF USE, DATA, OR PROFITS; OR BUSINESS INTERRUPTION) HOWEVER
CAUSED AND ON ANY THEORY OF LIABILITY, WHETHER IN CONTRACT, STRICT LIABILITY,
OR TORT (INCLUDING NEGLIGENCE OR OTHERWISE) ARISING IN ANY WAY OUT OF THE USE
OF THIS SOFTWARE, EVEN IF ADVISED OF THE POSSIBILITY OF SUCH DAMAGE.

-------------------------------------------------------------------------
### PFFFT
https://bitbucket.org/jpommier/pffft

BSD-like (FFTPACK5 / UCAR licence). Vendored into `engine/convolver/`
(`pffft.c`, `pffft.h`) as the convolver FFT on Linux; the full licence
text is in those file headers.

Copyright (c) 2013 Julien Pommier ( pommier@modartt.com )
Based on original fortran 77 code from FFTPACKv4 from NETLIB,
authored by Dr Paul Swarztrauber of NCAR, in 1985. As confirmed by the
NCAR fftpack software curators, the FFTPACKv5 license applies to
FFTPACKv4 sources; the C translation is released under the same terms.

Copyright (c) 2004 the University Corporation for Atmospheric Research
("UCAR"). All rights reserved. Developed by NCAR's Computational and
Information Systems Laboratory, UCAR, www.cisl.ucar.edu.

Redistribution and use of the Software in source and binary forms, with
or without modification, is permitted provided that the following
conditions are met:

- Neither the names of NCAR's Computational and Information Systems
Laboratory, the University Corporation for Atmospheric Research, nor the
names of its sponsors or contributors may be used to endorse or promote
products derived from this Software without specific prior written
permission.

- Redistributions of source code must retain the above copyright notices,
this list of conditions, and the disclaimer below.

- Redistributions in binary form must reproduce the above copyright
notice, this list of conditions, and the disclaimer below in the
documentation and/or other materials provided with the distribution.

THIS SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS
OR IMPLIED, INCLUDING, BUT NOT LIMITED TO THE WARRANTIES OF
MERCHANTABILITY, FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN
NO EVENT SHALL THE CONTRIBUTORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY
CLAIM, INDIRECT, INCIDENTAL, SPECIAL, EXEMPLARY, OR CONSEQUENTIAL DAMAGES
OR OTHER LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE,
ARISING FROM, OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR
OTHER DEALINGS WITH THE SOFTWARE.

## Data embedded in the binary

-------------------------------------------------------------------------
### TH Koln KU100 far-field HRIR compilation
https://zenodo.org/records/3928297

CC BY 3.0. The binaural renderer's default HRTF filter set
(`engine/hrtf/sets/thk-ku100-bimagls-v1.pahrtf`, embedded in the binary)
is derived from this dataset: the measured HRIRs, ear-aligned and
encoded to ambisonic filter banks by Panaudia's offline tool. The
derivation is recorded in the set's manifest.

Attribution: B. Bernschuetz, "A Spherical Far Field HRIR/HRTF
Compilation of the Neumann KU 100", TH Koln, 2013.
https://zenodo.org/records/3928297 — licensed under the Creative
Commons Attribution 3.0 Unported licence,
https://creativecommons.org/licenses/by/3.0/.

## Container image

The runtime image is Debian (trixie-slim) with the libopus0 and
ca-certificates packages. The licences of those packages and of the
base system are in /usr/share/doc/*/copyright inside the image, per
Debian policy; they are not reproduced here.

"""

if __name__ == "__main__":
    main()
