package history

import "strings"

// stopwords are the function words the word cloud leaves out: they top every
// count and say nothing about what you dictate. Dutch and English, the
// languages Vito is mostly dictated in; words of three letters or fewer are
// dropped before this list is consulted, so only longer ones need to be here.
var stopwords = func() map[string]bool {
	m := map[string]bool{}
	for _, w := range strings.Fields(`
		aan aangezien achter alle alleen allemaal alles also altijd anders
		bent beide best betreft bijna binnen boven daar daarbij daarin daarna
		daarom daarop daarvan dan dat deze dezelfde dicht dient dient doen doet
		door dus echt eens eerst eigen eigenlijk elke enige enkel erg even
		gaan gaat geen geeft geweest gewoon goed graag heb hebben hebt heeft
		hele hier hierbij hierin hierna hiervoor hoe hoewel hun iemand iets
		ieder jullie jouw kan kijk kijken komen komt kon konden kunnen kunt
		laat laten liever maak maakt maar maken mag meer mijn minder misschien
		moet moeten mogen mijn naar nadat natuurlijk niet niets noch nodig
		nog nogal omdat onder ongeveer onze ook over overal paar precies
		sinds steeds terwijl toch toen tot tussen uit vaak van vanaf vanuit
		veel verder vond voor vooral voordat waar waarbij waardoor waarin
		waarom wanneer want waren was wat welk welke werd werden wie wij
		wil wilde willen word worden wordt zal zeer zelf zich zichzelf zien
		ziet zij zijn zit zitten zo'n zoals zodat zonder zou zouden zowel
		zullen staat staan stond geval manier ander andere anderen eerste tweede
		beetje weer heel helemaal keer zeggen zegt gezegd moment echter

		about above after again against all also although always another
		anything around back because been before being below between both
		cannot could couldn't didn't does doesn't doing don't done down
		during each else even ever every from further going gonna have
		haven't having here hers herself himself into isn't it's itself just
		know like make many might more most much must myself need never
		only other ought ours ourselves over really same shall she'll should
		shouldn't since some something still such than that that's their
		theirs them themselves then there there's these they they're thing
		things think this those though through very want wasn't well were
		weren't what what's when where which while will with without won't
		would wouldn't your yours yourself yourselves yeah okay
	`) {
		m[w] = true
	}
	return m
}()
