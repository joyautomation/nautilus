// Packing a screen's tag list into the handful of patterns one subscription
// accepts.
//
// The property that matters is the SUPERSET one, and it is the only kind of
// bug here that does not announce itself: a pattern set that has quietly
// dropped a tag renders a live instrument as "—" forever, on a screen that
// otherwise looks healthy. Every spec below either checks that property
// directly or checks a rule that exists to protect it, and the fleet fixture
// at the bottom runs it over the real Pomona name-space — 217 tags from
// `/system`, which is what forced the cap problem in the first place.
import { describe, it, expect } from './harness.js';
import {
	MAX_TAG_PATTERNS,
	NO_TAGS,
	mergeTagPatterns,
	packTagPatterns,
	tagInPatterns,
	tagPatternMatches
} from '../src/lib/tags.js';

describe('tagPatternMatches — path.Match semantics', () => {
	it('an exact name matches only itself', () => {
		expect(tagPatternMatches('RTU9_WEL15_FIT_001', 'RTU9_WEL15_FIT_001')).toBe(true);
		expect(tagPatternMatches('RTU9_WEL15_FIT_001', 'RTU9_WEL15_FIT_002')).toBe(false);
	});

	it('? is exactly one character', () => {
		expect(tagPatternMatches('RTU9_WEL15_FIT_00?', 'RTU9_WEL15_FIT_001')).toBe(true);
		expect(tagPatternMatches('RTU9_WEL15_FIT_00?', 'RTU9_WEL15_FIT_0012')).toBe(false);
		expect(tagPatternMatches('RTU9_WEL15_FIT_00?', 'RTU9_WEL15_FIT_00')).toBe(false);
	});

	it('* spans the rest of a dotted name — a tag path has no separator in it', () => {
		expect(tagPatternMatches('RTU32_*', 'RTU32_BPS9_BP_09A')).toBe(true);
		expect(tagPatternMatches('*', 'anything.at.all')).toBe(true);
		expect(tagPatternMatches('Tank*', 'Tank101.Level')).toBe(true);
	});

	it('a dot is an ordinary character, not a wildcard', () => {
		expect(tagPatternMatches('AB.C', 'ABxC')).toBe(false);
		expect(tagPatternMatches('AB.C', 'AB.C')).toBe(true);
	});

	it('an empty pattern list is the unfiltered stream', () => {
		expect(tagInPatterns([], 'RTU9_WEL15_FIT_001')).toBe(true);
		expect(tagInPatterns(['RTU9_*'], 'RTU32_BPS9_BP_09A')).toBe(false);
		expect(tagInPatterns(['RTU9_*', 'RTU32_*'], 'RTU32_BPS9_BP_09A')).toBe(true);
	});

	it('NO_TAGS matches nothing a controller would ever publish', () => {
		for (const n of ['RTU9_WEL15_FIT_001', 'AEP_RES6_FIT_001', 'ClockRate']) {
			expect(tagPatternMatches(NO_TAGS, n)).toBe(false);
		}
	});
});

describe('mergeTagPatterns — every merge widens', () => {
	it('same length, differing positions become ?', () => {
		expect(mergeTagPatterns('RTU9_WEL15_FIT_001', 'RTU9_WEL15_FIT_002')).toBe(
			'RTU9_WEL15_FIT_00?'
		);
	});

	it('different lengths fall back to the common literal prefix plus *', () => {
		expect(mergeTagPatterns('RTU32_BPS9_BP_09A', 'RTU32_RES2_LIT_001')).toBe('RTU32_*');
		expect(mergeTagPatterns('RTU32_BPS9', 'RTU32_BPS9_FIT_001')).toBe('RTU32_BPS9*');
	});

	it('a wildcard in either operand ends the literal prefix', () => {
		// The trap this rule exists for: `abc?` and `abcd` are the same
		// length, but a per-character merge would produce `abc?`, which does
		// NOT match everything `abc*` matches.
		expect(mergeTagPatterns('abc*', 'abcd')).toBe('abc*');
		expect(mergeTagPatterns('ab?d', 'abcde')).toBe('ab*');
	});

	it('merging a pattern with itself changes nothing', () => {
		expect(mergeTagPatterns('RTU9_*', 'RTU9_*')).toBe('RTU9_*');
	});
});

describe('packTagPatterns', () => {
	it('returns fewer names than the cap verbatim — exact names, exact frame', () => {
		const names = ['B', 'A', 'C'];
		expect(packTagPatterns(names, { max: 10 })).toEqual(['A', 'B', 'C']);
	});

	it('dedupes and drops empties', () => {
		expect(packTagPatterns(['A', 'A', '', 'B'], { max: 10 })).toEqual(['A', 'B']);
	});

	it('an empty list means NO tags, not every tag', () => {
		// `tags: []` is the unfiltered stream to RealtimeOptions, so a caller
		// with nothing to draw must not be handed one.
		expect(packTagPatterns([], { max: 10 })).toEqual([NO_TAGS]);
	});

	it('packs to at most `max` patterns', () => {
		const names = Array.from({ length: 200 }, (_, i) => `RTU${i}_WEL${i}_FIT_001`);
		expect(packTagPatterns(names, { max: 5 }).length <= 5).toBe(true);
		expect(packTagPatterns(names).length <= MAX_TAG_PATTERNS).toBe(true);
	});

	it('prefers a ? merge over a * merge — it drags in far less plant', () => {
		// Two families of four. A per-character merge collapses each family
		// into one `?` pattern and never has to truncate anything.
		const names = [
			'RTU9_WEL15_FIT_001',
			'RTU9_WEL15_FIT_002',
			'RTU9_WEL15_FIT_003',
			'RTU9_WEL15_FIT_004',
			'RTU32_BPS9_BP_09A',
			'RTU32_BPS9_BP_09B',
			'RTU32_BPS9_BP_09C',
			'RTU32_BPS9_BP_09D'
		];
		expect(packTagPatterns(names, { max: 2 })).toEqual([
			'RTU32_BPS9_BP_09?',
			'RTU9_WEL15_FIT_00?'
		]);
	});
});

// The fixture is the real thing: every top-level tag `/system` binds on the
// Pomona central host, which is 217 names against a 40-pattern cap. It is
// here because the property under test only breaks at scale — with a handful
// of names nothing is ever merged.
const POMONA_SYSTEM = [
	'AEP_RES6_AIT_001_NO3',
	'AEP_RES6_FIT_001',
	'AEP_TP_AIT_004_NO3',
	'PFP_FP_FIT_001',
	'PFP_FP_FIT_002',
	'RTU10_WEL16_FIT_001',
	'RTU10_WEL16_SUP_016',
	'RTU11_WEL17_FIT_001',
	'RTU11_WEL17_SUP_017',
	'RTU12_WEL18_FIT_001',
	'RTU12_WEL18_SUP_018',
	'RTU13_WEL21_FIT_001',
	'RTU13_WEL21_SUP_021',
	'RTU14_WEL23_FIT_001',
	'RTU14_WEL23_SUP_023',
	'RTU15_WEL25_FIT_001',
	'RTU15_WEL25_SUP_025',
	'RTU17_WEL19_FIT_001',
	'RTU17_WEL19_SUP_019',
	'RTU17_ZONE8_PIT_001',
	'RTU19_WEL26_FIT_001',
	'RTU19_WEL26_SUP_026',
	'RTU20_WEL27_FIT_001',
	'RTU20_WEL27_SUP_027',
	'RTU21_WEL28_FIT_001',
	'RTU21_WEL28_SUP_028',
	'RTU22_6XFR7_FIT_001',
	'RTU22_6XFR7_GLV_001',
	'RTU22_ZONE6_PIT_001',
	'RTU23_BPS6_BP_06A',
	'RTU23_BPS6_BP_06B',
	'RTU23_BPS6_FIT_001',
	'RTU24_BPS1_BP_01A',
	'RTU24_BPS1_BP_01B',
	'RTU24_BPS1_BP_01C',
	'RTU24_BPS1_FIT_001',
	'RTU25A_7XFR5_FIT_001',
	'RTU25A_7XFR5_GLV_001',
	'RTU25A_BPS2_BP_02A',
	'RTU25A_BPS2_BP_02B',
	'RTU25A_BPS2_BP_02C',
	'RTU25A_BPS2_FIT_02A',
	'RTU25B_BPS2_BP_02D',
	'RTU25B_BPS2_BP_02E',
	'RTU25B_BPS2_FIT_02D',
	'RTU25B_RES5_LIT_001',
	'RTU25B_RES5_LIT_002',
	'RTU26A_BPS3_BP_03A',
	'RTU26A_BPS3_BP_03B',
	'RTU26A_BPS3_FIT_001',
	'RTU26B_BPS3_BP_03C',
	'RTU26B_BPS3_BP_03D',
	'RTU26C_WEL5_FIT_001',
	'RTU26C_WEL5_SUP_005',
	'RTU27_BPS5_BP_05A',
	'RTU27_BPS5_BP_05B',
	'RTU27_BPS5_FIT_001',
	'RTU28_BPS10_BP_10A',
	'RTU28_BPS10_BP_10B',
	'RTU28_NEWZONE5_PIT_001',
	'RTU2_WEL2_FIT_001',
	'RTU2_WEL2_SUP_002',
	'RTU31_4XFRMWD_FIT_001',
	'RTU31_BPS8_BP_08A',
	'RTU31_BPS8_BP_08B',
	'RTU31_BPS8_FIT_001',
	'RTU32_BPS9_BP_09A',
	'RTU32_BPS9_BP_09B',
	'RTU32_BPS9_FIT_001',
	'RTU32_RES2_LIT_001',
	'RTU32_WEL9_FIT_001',
	'RTU32_WEL9_SUP_001',
	'RTU33A_11XFR8_FIT_001',
	'RTU33A_BPS11_FIT_11A',
	'RTU33A_RES10_LIT_001',
	'RTU33A_ZONE11_PIT_001',
	'RTU33B_BPS11_FIT_11B',
	'RTU33B_RES10_LIT_002',
	'RTU34_HYDRO1_JI_001',
	'RTU35_12XFR8_FIT_001',
	'RTU35_BPS12_BP_12A',
	'RTU35_BPS12_BP_12B',
	'RTU35_ZONE12_PIT_001',
	'RTU36_HYDRO12_JI_001',
	'RTU37_WEL32_FIT_001',
	'RTU37_WEL32_SUP_032',
	'RTU38_GBPXFR_FIT_001',
	'RTU38_GBPXFR_GLV_001',
	'RTU39_8XFR6_FIT_001',
	'RTU3_WEL3_FIT_001',
	'RTU3_WEL3_SUP_003',
	'RTU40_RES3_LIT_001',
	'RTU41_RES4_LIT_001',
	'RTU41_RES4_LIT_002',
	'RTU42_2XFR7_FIT_001',
	'RTU42_2XFR7_GLV_001',
	'RTU42_RES7_LIT_001',
	'RTU42_ZONE2_PIT_001',
	'RTU43_RES9_LIT_001',
	'RTU43_WEL20_FIT_001',
	'RTU43_WEL20_SUP_020',
	'RTU44A_13XFR9_FIT_001',
	'RTU44B_RES13_LIT_001',
	'RTU45_RECLM_BP_001',
	'RTU45_RECLM_FIT_CDEF',
	'RTU46_8XFRRCLM_FIT_001',
	'RTU46_RESRCL_LIT_001',
	'RTU48_MWD5_FIT_001',
	'RTU50_WEL8_FIT_001',
	'RTU50_WEL8_SUP_008',
	'RTU51_WEL35_FIT_001',
	'RTU51_WEL35_SUP_035',
	'RTU52_WEL36_FIT_001',
	'RTU52_WEL36_SUP_036',
	'RTU53_WEL37_FIT_001',
	'RTU53_WEL37_SUP_037',
	'RTU58_BPS15_BP_15A',
	'RTU5_WEL7_FIT_001',
	'RTU5_WEL7_SUP_007',
	'RTU60_13XFR9_GLV_001',
	'RTU60_ZONE13_PIT_001',
	'RTU61_WEL34_FIT_001',
	'RTU61_WEL34_SUP_034',
	'RTU6_WEL10_FIT_001',
	'RTU6_WEL10_SUP_010',
	'RTU7_WEL13_FIT_001',
	'RTU7_WEL13_SUP_013',
	'RTU9_WEL15_FIT_001',
	'RTU9_WEL15_SUP_015'
];

describe('packTagPatterns — the Pomona /system subscription', () => {
	it('fits the controller cap', () => {
		const pats = packTagPatterns(POMONA_SYSTEM);
		expect(pats.length <= MAX_TAG_PATTERNS).toBe(true);
	});

	it('still matches every tag the screen draws', () => {
		// THE property. A dropped tag is a dead-looking instrument, and
		// nothing else in the stack would notice.
		const pats = packTagPatterns(POMONA_SYSTEM);
		const missed = POMONA_SYSTEM.filter((n) => !tagInPatterns(pats, n));
		expect(missed).toEqual([]);
	});

	it('holds the property at every cap, down to one pattern', () => {
		for (const max of [1, 2, 5, 10, 20, 40, 80]) {
			const pats = packTagPatterns(POMONA_SYSTEM, { max });
			expect(pats.length <= max).toBe(true);
			expect(POMONA_SYSTEM.filter((n) => !tagInPatterns(pats, n))).toEqual([]);
		}
	});

	it('does not reach for * while a ? merge is available', () => {
		// At the real cap the greedy loop can still collapse whole families
		// character by character, so most of the set stays narrow. This is
		// what separates a 200 kB subscription from a 500 kB one; if it ever
		// regresses, the packer got the cost function wrong.
		const pats = packTagPatterns(POMONA_SYSTEM);
		const wide = pats.filter((p) => p.includes('*'));
		expect(wide.length <= 4).toBe(true);
	});
});
