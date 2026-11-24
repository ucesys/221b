// Star us on GitHub (0.14.0, CU-21): whether the dialog asks the person
// signed in, a SuperAdmin the Helm values name, and its Never show this
// again. The console never calls GitHub and keeps nothing about starring.
import { call } from './call'

export interface StarPrompt {
  show: boolean
  /** A key of the session (a hash of its ID): the dialog closed under it shows again at the next sign-in. */
  session?: string
}

/** The repository the dialog links to. */
export const REPO_URL = 'https://github.com/ucesys/221b'

export const fetchStarPrompt = () => call<StarPrompt>('/api/v1/star-prompt', {}, true)

/** Never show this again: the console-wide setting off, audited; a SuperAdmin turns it on in Administration › Settings › General. */
export const neverStarPrompt = () => call<StarPrompt>('/api/v1/star-prompt/never', { method: 'POST', body: '{}' })
