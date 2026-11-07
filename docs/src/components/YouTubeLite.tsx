// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import React, {useState} from 'react';
import {translate} from '@docusaurus/Translate';
import styles from './YouTubeLite.module.css';

interface Props {
  /** The YouTube video id. */
  id: string;
  /** The video's title, used for the button label and the iframe title. */
  title: string;
}

// Click-to-load: the page makes no request to YouTube beyond the thumbnail image until the
// reader asks for the video, and then loads it from the privacy-enhanced nocookie domain.
export default function YouTubeLite({id, title}: Props) {
  const [active, setActive] = useState(false);
  const watchUrl = `https://youtu.be/${id}`;
  const playLabel = translate(
    {id: 'youtubeLite.play', message: 'Play video: {title}'},
    {title},
  );
  return (
    <div className={styles.wrapper}>
      <div className={styles.frame}>
        {active ? (
          <iframe
            className={styles.fill}
            src={`https://www.youtube-nocookie.com/embed/${id}?autoplay=1`}
            title={title}
            allow="autoplay; encrypted-media; picture-in-picture; fullscreen"
            allowFullScreen
            referrerPolicy="strict-origin-when-cross-origin"
          />
        ) : (
          <button
            type="button"
            className={`${styles.fill} ${styles.trigger}`}
            aria-label={playLabel}
            onClick={() => setActive(true)}
            style={{backgroundImage: `url(https://i.ytimg.com/vi/${id}/hqdefault.jpg)`}}>
            <span className={styles.play} aria-hidden="true" />
          </button>
        )}
      </div>
      <p className={styles.fallback}>
        <a href={watchUrl} target="_blank" rel="noopener noreferrer">
          {translate({id: 'youtubeLite.watchOnYouTube', message: 'Watch on YouTube'})}
        </a>
      </p>
    </div>
  );
}
