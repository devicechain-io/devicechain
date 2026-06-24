/**
 * Copyright © 2022 DeviceChain
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *     http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

package proto

// Creates a uint64* from uint*.
func NullUint64Of(value *uint) *uint64 {
	if value != nil {
		conv := uint64(*value)
		return &conv
	}
	return nil
}

// Creates a uint* from uint64*.
func NullUintOf(value *uint64) *uint {
	if value != nil {
		conv := uint(*value)
		return &conv
	}
	return nil
}
