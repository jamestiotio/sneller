// Copyright (C) 2022 Sneller, Inc.
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU Affero General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// This program is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
// GNU Affero General Public License for more details.
//
// You should have received a copy of the GNU Affero General Public License
// along with this program.  If not, see <http://www.gnu.org/licenses/>.

package main

import (
	"fmt"
	"io"
	"net/http"
)

func (s *server) versionHandler(w http.ResponseWriter, r *http.Request) {
	endPoints := s.peers.Get()
	w.Header().Add("Content-Type", "text/plain")
	w.WriteHeader(http.StatusOK)
	io.WriteString(w, fmt.Sprintf("Sneller daemon %s (cluster size: %d nodes)", version, len(endPoints)))
}
