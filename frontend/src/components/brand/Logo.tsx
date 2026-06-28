// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import type { SVGProps } from 'react';

// DeviceChain logo — geometry generated from branding/logos/logo.svg (outlined
// paths; do not hand-edit the shape data). Three exports share one geometry:
//   <Logo>      full lockup — cube over the "DeviceChain" wordmark
//   <Logotype>  wordmark only
//   <Logomark>  cube only — icon / favicon
//
// Wordmark colors are injectable and default to the standard brand colors. Pass
// deviceColor="currentColor" to make the wordmark follow the theme (light/dark).
// The cube keeps its brand palette unless `mono` overrides every facet with one
// color (e.g. "currentColor") for single-color contexts.

const DEVICE = '#FFFFFF';
const CHAIN = '#208CB7';

type WordmarkProps = SVGProps<SVGSVGElement> & {
  deviceColor?: string;
  chainColor?: string;
};

type MarkProps = SVGProps<SVGSVGElement> & {
  mono?: string;
};

// "Device" — the half of the wordmark that flips for light/dark backgrounds.
function Device({ color }: { color: string }) {
  return (
    <>
      <path fill={color} d="M67.04,191.19c-1.94-0.97-4.17-1.46-6.65-1.46H50v23.62h10.39c2.48,0,4.72-0.49,6.65-1.46 c1.95-0.98,3.49-2.38,4.57-4.16c1.08-1.78,1.62-3.87,1.62-6.19c0-2.32-0.55-4.4-1.62-6.19C70.53,193.57,68.99,192.16,67.04,191.19 z M55.2,194.29h4.99c1.57,0,2.97,0.3,4.15,0.9c1.16,0.59,2.07,1.43,2.7,2.52c0.63,1.09,0.95,2.38,0.95,3.84 c0,1.45-0.32,2.75-0.95,3.84c-0.63,1.08-1.53,1.93-2.7,2.52c-1.18,0.6-2.58,0.9-4.15,0.9H55.2V194.29z"/>
      <polygon fill={color} points="84.03,203.59 94.95,203.59 94.95,199.13 84.03,199.13 84.03,194.26 96.34,194.26 96.34,189.73 78.83,189.73 78.83,213.35 96.8,213.35 96.8,208.83 84.03,208.83"/>
      <polygon fill={color} points="111.68,206.42 104.48,189.73 98.83,189.73 109.16,213.35 113.95,213.35 124.24,189.73 118.95,189.73"/>
      <rect x="127.5" y="189.73" fill={color} width="5.2" height="23.62"/>
      <path fill={color} d="M147.22,195.05c1.15-0.63,2.47-0.95,3.93-0.95c2.23,0,4.14,0.84,5.67,2.49l0.34,0.37l3.43-3.23l-0.31-0.36 c-1.1-1.28-2.47-2.27-4.06-2.95c-1.58-0.67-3.36-1.01-5.27-1.01c-2.36,0-4.52,0.53-6.43,1.56c-1.91,1.04-3.44,2.5-4.54,4.34 c-1.1,1.84-1.65,3.93-1.65,6.23c0,2.3,0.55,4.4,1.64,6.23c1.09,1.84,2.61,3.3,4.52,4.34c1.91,1.04,4.07,1.56,6.43,1.56 c1.91,0,3.69-0.34,5.28-1.01c1.6-0.67,2.98-1.66,4.08-2.95l0.31-0.36l-3.43-3.26l-0.35,0.38c-1.53,1.67-3.44,2.52-5.67,2.52 c-1.46,0-2.78-0.32-3.93-0.95c-1.14-0.63-2.04-1.52-2.68-2.64c-0.64-1.12-0.97-2.42-0.97-3.85c0-1.43,0.33-2.73,0.97-3.85 C145.18,196.57,146.08,195.68,147.22,195.05z"/>
      <polygon fill={color} points="170.78,203.59 181.7,203.59 181.7,199.13 170.78,199.13 170.78,194.26 183.09,194.26 183.09,189.73 165.58,189.73 165.58,213.35 183.55,213.35 183.55,208.83 170.78,208.83"/>
    </>
  );
}

// "Chain" — the brand-blue half of the wordmark.
function Chain({ color }: { color: string }) {
  return (
    <>
      <path fill={color} d="M195.48,193.32L195.48,193.32c1.47-0.82,3.13-1.23,4.93-1.23c2.67,0,4.89,0.85,6.59,2.54l0.36,0.36l1.72-1.77 l-0.33-0.35c-1.02-1.08-2.26-1.91-3.71-2.47c-1.43-0.55-3.01-0.82-4.7-0.82c-2.29,0-4.39,0.52-6.24,1.55 c-1.85,1.03-3.33,2.47-4.39,4.29c-1.06,1.81-1.6,3.88-1.6,6.13c0,2.26,0.54,4.32,1.6,6.13c1.06,1.82,2.54,3.26,4.39,4.29 c1.84,1.03,3.94,1.55,6.24,1.55c1.67,0,3.25-0.28,4.69-0.84c1.45-0.56,2.71-1.4,3.72-2.48l0.33-0.35l-1.72-1.77l-0.36,0.36 c-1.73,1.71-3.94,2.57-6.59,2.57c-1.8,0-3.46-0.41-4.93-1.23c-1.46-0.81-2.63-1.95-3.46-3.38c-0.83-1.43-1.26-3.06-1.26-4.84 s0.42-3.4,1.26-4.84C192.85,195.27,194.01,194.13,195.48,193.32z"/>
      <polygon fill={color} points="232.32,200.14 218.2,200.14 218.2,189.73 215.55,189.73 215.55,213.35 218.2,213.35 218.2,202.59 232.32,202.59 232.32,213.35 234.97,213.35 234.97,189.73 232.32,189.73"/>
      <path fill={color} d="M253.08,189.73h-2.29l-10.87,23.62h2.88l2.88-6.37h12.47l2.91,6.37h2.88l-10.73-23.33L253.08,189.73z M257.09,204.59h-10.3l5.14-11.34L257.09,204.59z"/>
      <rect x="268.91" y="189.73" fill={color} width="2.65" height="23.62"/>
      <polygon fill={color} points="297.35,189.73 297.35,208.43 282.72,189.73 280.58,189.73 280.58,213.35 283.23,213.35 283.23,194.66 297.75,213.16 297.9,213.35 300,213.35 300,189.73"/>
    </>
  );
}

// The isometric cube brandmark. Facets default to the brand palette; `mono`
// collapses them to a single flat color.
function Cube({ mono }: { mono?: string }) {
  const f = (brand: string) => mono ?? brand;
  return (
    <>
      <path fill={f("#1F425E")} d="M175,66.32l-45,25.98v51.96l45,25.98l45-25.98V92.31L175,66.32z M201.47,133.57L175,148.85l-26.47-15.28v0 v-2.29V105.3V103v0L175,87.72L201.47,103v0V133.57L201.47,133.57z"/>
      <polygon fill={f("#7AB7D9")} points="201.47,103 201.47,103 220,92.31 175,66.32 175,87.72"/>
      <polygon fill={f("#208CB7")} points="148.53,131.28 148.53,105.3 148.53,103 130,92.31 130,144.27 148.53,133.57"/>
      <polygon fill={f("#52A2C9")} points="148.53,103 175,87.72 175,66.32 130,92.31 148.53,103"/>
      <polygon fill={f("#007BA6")} points="175,170.25 175,148.85 148.53,133.57 148.53,133.57 130,144.27 175,170.25"/>
      <polygon fill={f("#006790")} points="175,170.25 220,144.27 201.47,133.57 201.47,133.57 175,148.85 175,170.25"/>
      <polygon fill={f("#9ACEEC")} points="201.47,103 201.47,133.57 220,144.27 220,92.31"/>
      <polygon fill={f("#006B97")} points="175,92.31 152.5,105.3 152.5,131.28 175,144.27 197.5,131.28 197.5,105.3"/>
      <polygon fill={f("#006790")} points="197.5,105.3 175,92.31 175,118.29"/>
      <polygon fill={f("#007BA6")} points="175,92.31 152.5,105.3 175,118.29"/>
      <polygon fill={f("#9ACEEC")} points="152.5,105.3 152.5,131.28 175,144.27 175,118.29"/>
      <polygon fill={f("#208CB7")} points="175,118.29 175,144.27 197.5,131.28 197.5,105.3"/>
    </>
  );
}

// Full lockup: cube over the wordmark.
export function Logo({ deviceColor = DEVICE, chainColor = CHAIN, ...props }: WordmarkProps) {
  return (
    <svg viewBox="50 66.32 250 147.03" xmlns="http://www.w3.org/2000/svg" role="img" aria-label="DeviceChain" {...props}>
      <Cube />
      <Device color={deviceColor} />
      <Chain color={chainColor} />
    </svg>
  );
}

// Wordmark only.
export function Logotype({ deviceColor = DEVICE, chainColor = CHAIN, ...props }: WordmarkProps) {
  return (
    <svg viewBox="50 189.73 250 23.62" xmlns="http://www.w3.org/2000/svg" role="img" aria-label="DeviceChain" {...props}>
      <Device color={deviceColor} />
      <Chain color={chainColor} />
    </svg>
  );
}

// Cube mark only (icon / favicon).
export function Logomark({ mono, ...props }: MarkProps) {
  return (
    <svg viewBox="123.03 66.32 103.93 103.93" xmlns="http://www.w3.org/2000/svg" role="img" aria-label="DeviceChain" {...props}>
      <Cube mono={mono} />
    </svg>
  );
}
