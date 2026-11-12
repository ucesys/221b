// The tables of AIOps › Mycroft's sub-tabs are one size (build 14): each
// fills the column in a card that scrolls sideways when narrower, and
// none is narrower than this, so switching sub-tabs keeps the table's
// size. Exceptions set it first; Wiki (its sources), Known issues, Fixes
// and Validation rules take the same, through MycroftPage's MycroftTable.
import type { CSSProperties } from 'react'

/** The narrowest a Mycroft table gets, in pixels. */
export const MYCROFT_TABLE_MIN = 980

/** The style every Mycroft table takes (tb-table fills its card's width). */
export const MYCROFT_TABLE: CSSProperties = { minWidth: MYCROFT_TABLE_MIN }
