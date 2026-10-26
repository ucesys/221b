import { createContext, useContext } from 'react'
import type { ToolName, ToolRequest } from '../../api/toolbox'

/** What a tool's dialog opens on: a pod, or a node, and what to fill in. */
export interface OpenTool {
  tool: ToolName
  pod?: { name: string; namespace: string }
  node?: string
  /** Values the dialog starts with: a container, a port, a command (Sherlock's, TB-41). */
  prefill?: Partial<ToolRequest>
  /** Where the request came from, for the card: the Tools menu, Sherlock. */
  from?: string
}

export interface ToolboxApi {
  /** Whether tools can be asked for here: Watson is on (the cards go there). */
  available: boolean
  /** Why not, when not. */
  off?: string
  cluster: string
  openTool: (o: OpenTool) => void
  /** Hands a kubectl line to Watson (Cordon, Drain): every command from the UI goes through it. */
  watson: (command: string, from: string) => void
  /** Brings a session's tab forward in the dock. */
  showSession: (id: string) => void
}

export const ToolboxContext = createContext<ToolboxApi | null>(null)

export const useToolbox = () => useContext(ToolboxContext)
