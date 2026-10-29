// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using DeviceChain.Towerpulse.Story;
using UnityEngine;

namespace DeviceChain.Towerpulse.Visuals
{
    /// <summary>
    /// Plays the shot file: one camera, moved along the storyboard's beats. The file is validated whole before anything plays; a file that does not validate
    /// is reported (<see cref="Error"/>, and the console) and the camera is left where it is, never half-played. Show time is its own clock, separate from
    /// the model's, so a shot can be held, scrubbed or looped without touching the building.
    /// </summary>
    [DefaultExecutionOrder(80)]
    public sealed class ShotDirector : MonoBehaviour
    {
        [SerializeField] TextAsset shotFile;
        [SerializeField] Camera director;
        [SerializeField] bool playOnStart = true;
        [SerializeField] bool loop;

        ShotList shots;

        public string Error { get; private set; }
        public ShotList Shots => shots;
        public double ShowTime { get; private set; }
        public bool Playing { get; set; }
        public bool Loop { get { return loop; } set { loop = value; } }
        public Shot Current { get; private set; }
        public Camera Camera => director;

        public void Configure(TextAsset file, Camera cam)
        {
            shotFile = file;
            director = cam;
        }

        void Start()
        {
            Load();
            Playing = playOnStart && Error == null;
        }

        public bool Load()
        {
            Error = null;
            shots = null;
            if (shotFile == null) { Error = "no shot file is assigned"; Debug.LogError("[towerpulse] " + Error); return false; }
            var res = ShotFile.Parse(shotFile.text);
            if (!res.Ok)
            {
                Error = "the shot file is not valid: " + res.Report();
                Debug.LogError("[towerpulse] " + Error);
                return false;
            }
            shots = res.List;
            return true;
        }

        void Update()
        {
            if (shots == null) return;
            if (Playing)
            {
                ShowTime += Time.unscaledDeltaTime;
                if (ShowTime > shots.TotalS)
                {
                    if (loop) ShowTime -= shots.TotalS;
                    else { ShowTime = shots.TotalS; Playing = false; }
                }
            }
            Apply();
        }

        /// <summary>Puts the camera where the shot file says it is at a show time, and leaves it paused there.</summary>
        public void Seek(double showS)
        {
            if (shots == null && !Load()) return;
            ShowTime = showS < 0 ? 0 : showS;
            Playing = false;
            Apply();
        }

        void Apply()
        {
            if (director == null) return;
            if (!shots.TryPose(ShowTime, out var p, out var shot)) { Current = null; return; }
            Current = shot;
            var pos = new Vector3((float)p.Px, (float)p.Py, (float)p.Pz);
            var look = new Vector3((float)p.Lx, (float)p.Ly, (float)p.Lz);
            director.transform.position = pos;
            director.transform.rotation = Quaternion.LookRotation(look - pos, Vector3.up);
            director.fieldOfView = (float)p.Fov;
        }
    }
}
